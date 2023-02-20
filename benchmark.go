package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/lib/pq"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var n_conns = flag.Int("c", 1, "the number of connections")
var n_reqs = flag.Int("n", 10000, "the number of requests")
var n_keys = flag.Int("k", 10000, "the number of random keys")
var db_type = flag.String("db", "etcd", "db type")
var db_url = flag.String("url", "127.0.0.1:2379", "db url")
var is_watch = flag.Bool("watch", false, "watch delay benchmark")

var value = "foobar"

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func RandStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

type DB interface {
	put(k, v string) int64
	get(k string)
	init_watch()
	watch(cnt int)
}

type etcd struct {
	cli       *clientv3.Client
	watcher   clientv3.Watcher
	watchChan clientv3.WatchChan
}

func new_etcd() DB {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{*db_url},
		DialTimeout: 2 * time.Second,
	})
	if err != nil {
		panic(err)
	}
	return &etcd{cli: cli}
}

func (db *etcd) put(k, v string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	resp, err := db.cli.Put(ctx, k, v)
	cancel()
	if err != nil {
		panic(err)
	}
	return resp.Header.Revision
}

func (db *etcd) get(k string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	resp, err := db.cli.Get(ctx, k)
	cancel()
	if err != nil {
		panic(err)
	}
	if string(resp.Kvs[0].Key) != k || string(resp.Kvs[0].Value) != value {
		panic("get failed")
	}
}

func (db *etcd) init_watch() {
	db.watcher = clientv3.NewWatcher(db.cli)
	db.watchChan = db.watcher.Watch(context.Background(), "/routes/", clientv3.WithPrefix())
}

func (db *etcd) watch(cnt int) {
	i := 0
	for resp := range db.watchChan {
		for _, _ = range resp.Events {
			//if string(ev.Kv.Key) == k && ev.Kv.ModRevision == rev {
			//    return
			//}
			i += 1
			if i == cnt {
				return
			}
		}
	}
}

type RouteConfig struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Revision  int64  `json:"revision"`
	Tombstone bool   `json:"tombstone"`
}

type postgres struct {
	db       *sql.DB
	listener *pq.ListenerConn
	notify   chan *pq.Notification
}

func new_postgres() DB {
	db, err := sql.Open("postgres", *db_url)
	if err != nil {
		log.Fatal(err)
	}
	err = db.Ping()
	if err != nil {
		log.Fatal(err)
	}
	return &postgres{db: db}
}

func (db *postgres) put(k, v string) int64 {
	row := db.db.QueryRow(fmt.Sprintf(`select * from set('%s', '%s')`, k, v))
	var rev int64
	err := row.Scan(&rev)
	if err != nil {
		log.Fatal(err)
	}
	return rev
}

func (db *postgres) get(k string) {
	row := db.db.QueryRow(fmt.Sprintf(`select * from get1('%s')`, k))
	var rev int64
	var key string
	var value string
	err := row.Scan(&rev, &key, &value)
	if err != nil {
		log.Fatal(err)
	}
}

func (db *postgres) init_watch() {
	//listener := pq.NewListener(*db_url, 3*time.Second, 10*time.Second, nil)
	db.notify = make(chan *pq.Notification)
	listener, err := pq.NewListenerConn(*db_url, db.notify)
	if err != nil {
		panic(err)
	}
	ok, err := listener.ExecSimpleQuery("select pg_advisory_lock_shared(9080)")
	if !ok || err != nil {
		panic(err)
	}
	_, err = listener.Listen("routes")
	if err != nil {
		panic(err)
	}
	db.listener = listener
}

func (db *postgres) watch(cnt int) {
	i := 0
	for {
		select {
		case n := <-db.notify:
			if n == nil {
				panic("reconnect")
			}

			var cfg RouteConfig
			if err := json.Unmarshal([]byte(n.Extra), &cfg); err != nil {
				log.Fatalf("notification invalid: %s err=%v", n.Extra, err)
			}

			i += 1
			if i == cnt {
				return
			}
		}
	}
}

func main() {
	flag.Parse()
	flag.VisitAll(func(f *flag.Flag) {
		fmt.Printf("%-30s: %s\n", f.Usage, f.Value)
	})

	rand.Seed(time.Now().UnixNano())

	var db_list []DB
	for i := 0; i < *n_conns; i++ {
		switch *db_type {
		case "etcd":
			db_list = append(db_list, new_etcd())
		case "postgres":
			db_list = append(db_list, new_postgres())
		default:
		}
	}

	var keys []string
	log.Println("populate keys...")
	for i := 0; i < *n_keys; i++ {
		key := fmt.Sprintf("/routes/%s", RandStringRunes(100))
		keys = append(keys, key)
		db_list[0].put(key, value)
	}
	log.Println("done.")

	steps := []string{"put", "get"}
	for step := 2; step < len(steps); step++ {
		var wg sync.WaitGroup
		start := time.Now()
		for n := 0; n < *n_conns; n++ {
			wg.Add(1)
			go func(db DB) {
				defer wg.Done()
				for i := 0; i < *n_reqs; i++ {
					key := keys[rand.Intn(len(keys))]
					switch step {
					case 0:
						db.put(key, value)
					case 1:
						db.get(key)
					default:
					}
				}
			}(db_list[n])
		}
		wg.Wait()
		end := time.Now()
		sum := end.Sub(start).Milliseconds()
		if sum > 1000 {
			log.Printf("%s: %.3f s\n", steps[step], float64(sum)/1000)
		} else {
			log.Printf("%s: %d ms\n", steps[step], sum)
		}
	}

	if *is_watch {
		db := db_list[0]
		var wg sync.WaitGroup
		start1 := time.Now()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < *n_reqs; i++ {
				key := keys[rand.Intn(len(keys))]
				db.put(key, value)
			}
		}()
		wg.Wait()
		end1 := time.Now()

		db.init_watch()
		start := time.Now()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < *n_reqs; i++ {
				key := keys[rand.Intn(len(keys))]
				db.put(key, value)
				db.watch(1)
			}
		}()
		wg.Wait()
		end := time.Now()
		sum := end.Sub(start).Milliseconds() - end1.Sub(start1).Milliseconds()
		log.Printf("watch delay: %.3f ms\n", float64(sum)/float64(*n_reqs))
	}
}
