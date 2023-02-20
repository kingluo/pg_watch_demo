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
}

var db *sql.DB
var mutex sync.RWMutex
var routeConfigs = make(map[string]RouteConfig)
var routes = make(map[string]Route)
var latestRev int64 = 0

func updateRoute(cfg RouteConfig) {
	log.Printf("%+v\n", cfg)

	mutex.Lock()

	if cfg.Revision > latestRev {
		latestRev = cfg.Revision
	}

	tombstone := cfg.Tombstone

	if !tombstone {
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

	if !tombstone {
		log.Printf("add route: %s\n", val)
		routes[route.Uri] = route
	} else {
		log.Printf("del route: %s\n", val)
		delete(routes, route.Uri)
	}

	mutex.Unlock()
}

func watch(l *pq.Listener) {
	for {
		select {
		case n := <-l.Notify:
			if n == nil {
				log.Println("listener reconnected")
				log.Printf("get all routes from rev %d including tombstones...\n", latestRev)
				str := fmt.Sprintf(`select * from get_all_from_rev_with_stale('/routes/', %d)`, latestRev)
				rows, err := db.Query(str)
				if err != nil {
					panic(err)
				}

				for rows.Next() {
					var cfg RouteConfig
					var val sql.NullString
					err = rows.Scan(&cfg.Revision, &cfg.Key, &val, &cfg.Tombstone)
					if err != nil {
						panic(err)
					}
					if val.Valid {
						cfg.Value = val.String
					}
					updateRoute(cfg)
				}
				rows.Close()
				continue
			}

			var cfg RouteConfig
			if err := json.Unmarshal([]byte(n.Extra), &cfg); err != nil {
				log.Fatalf("notification invalid: %s err=%v", n.Extra, err)
			}
			if cfg.Revision <= latestRev {
				log.Println("Skip old route notification: ", n.Extra)
				continue
			}

			log.Printf("receive route notification: channel=%s: route: %s\n",
				n.Channel, n.Extra)

			updateRoute(cfg)
		case <-time.After(15 * time.Second):
			log.Println("Received no events for 15 seconds, checking connection")
			go func() {
				if err := l.Ping(); err != nil {
					log.Println("listener ping error: ", err)
				}
			}()
		}
	}
}

func main() {
	conninfo := "user=postgres password=postgres host=127.0.0.1 connect_timeout=5 sslmode=disable"
	var err error
	db, err = sql.Open("postgres", conninfo)
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

	listener := pq.NewListener(conninfo, 3*time.Second, 10*time.Second, reportProblem)
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
		err = rows.Scan(&cfg.Revision, &cfg.Key, &cfg.Value)
		if err != nil {
			panic(err)
		}
		updateRoute(cfg)
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
			req2, err := http.NewRequest("GET", route.Upstream+route.Uri, nil)
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
