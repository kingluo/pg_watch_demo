package main

import (
	"database/sql"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lib/pq"
)

type Route struct {
	Uri      string `json:"uri"`
	Upstream string `json:"upstream"`
}

type RouteConfig struct {
	Key        string `json:"key"`
	Value      string `json:"value"`
	Revision   int64  `json:"revision"`
	Tombstone  bool   `json:"tombstone"`
	CreateTime int64  `json:"create_time"`
}

var mutex sync.RWMutex
var routeConfigs = make(map[string]RouteConfig)
var routes = make(map[string]Route)
var startRev int64 = 0

func watch(l *pq.Listener) {
	for {
		select {
		case n := <-l.Notify:
			var cfg RouteConfig
			if err := json.Unmarshal([]byte(n.Extra), &cfg); err != nil {
                log.Fatalf("notification invalid: %s err=%v", n.Extra, err)
			}
			if cfg.Revision <= startRev {
				log.Println("Skip old route notification: ", n.Extra)
				continue
			}

			now := time.Now().UnixMilli()
			watch_delay := now - cfg.CreateTime
			log.Printf("receive route notification: channel=%s, watch_delay=%d milliseconds: route: %s\n",
				n.Channel, watch_delay, n.Extra)

			mutex.Lock()

			if !cfg.Tombstone {
				routeConfigs[cfg.Key] = cfg
			} else {
				cfg = routeConfigs[cfg.Key]
			}
			val, err := b64.StdEncoding.DecodeString(cfg.Value)
			if err != nil {
				panic(err)
			}
			var route Route
			if err := json.Unmarshal([]byte(val), &route); err != nil {
				log.Fatal(err)
			}

			if !cfg.Tombstone {
				log.Printf("add route: %s\n", val)
				routes[route.Uri] = route
			} else {
				log.Printf("del route: %s\n", val)
				delete(routes, route.Uri)
			}

			mutex.Unlock()
		case <-time.After(90 * time.Second):
			log.Println("Received no events for 90 seconds, checking connection")
			go func() {
				if err := l.Ping(); err != nil {
					log.Println("listener ping error: ", err)
				}
			}()
		}
	}
}

func main() {
	conninfo := "user=postgres password=postgres host=127.0.0.1 sslmode=disable"
	db, err := sql.Open("postgres", conninfo)
	if err != nil {
		log.Fatal(err)
	}
	err = db.Ping()
	if err != nil {
		log.Fatal(err)
	}

	// listen first
	// if listen happens after get_all, then it's possible to
	// lost new routes between get_all and listen.
	reportProblem := func(ev pq.ListenerEventType, err error) {
		if err != nil {
			log.Println(err.Error())
		}
	}

	listener := pq.NewListener(conninfo, 10*time.Second, time.Minute, reportProblem)
	err = listener.Listen("routes")
	if err != nil {
		panic(err)
	}

	log.Println("get all routes...")
	rows, err := db.Query(`select * from get_all('/routes/')`)
	if err != nil {
		panic(err)
	}

	for rows.Next() {
		var cfg RouteConfig
		err = rows.Scan(&cfg.Revision, &cfg.Key, &cfg.Value, &cfg.CreateTime)
		if err != nil {
			panic(err)
		}
		if cfg.Revision > startRev {
			startRev = cfg.Revision
		}
		routeConfigs[cfg.Key] = cfg

		val, err := b64.StdEncoding.DecodeString(cfg.Value)
		if err != nil {
			panic(err)
		}
		var route Route
		if err := json.Unmarshal([]byte(val), &route); err != nil {
			log.Fatal(err)
		}
		log.Println(route)
		routes[route.Uri] = route
	}
	rows.Close()

	// start handling route notifications
	log.Println("Start watching...")
	go watch(listener)

	// setup http server
	h1 := func(w http.ResponseWriter, req *http.Request) {
		mutex.RLock()
		route, ok := routes[req.URL.Path]
		mutex.RUnlock()
		if ok {
			log.Printf("%s -> %s\n", route.Uri, route.Upstream)
            req2, err := http.NewRequest("GET", route.Upstream + route.Uri, nil)
            if err != nil {
                for k, v := range req.Header {
                    if _, ok := req2.Header[k]; !ok {
                        req2.Header[k] = v
                    }
                }
            }
            client := &http.Client{}
            res, err := client.Do(req2)
			if err != nil {
				log.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
            for k, v := range res.Header {
                if _, ok := w.Header()[k]; !ok {
                    w.Header()[k] = v
                }
            }
			w.WriteHeader(res.StatusCode)
			fmt.Fprintf(w, string(body))
		} else {
			w.WriteHeader(404)
			fmt.Fprintln(w, "no route")
		}
	}

	http.HandleFunc("/", h1)

	listenAddr := ":9080"
	log.Printf("Start Data Plane, listen %s\n", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
