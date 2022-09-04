package main

import (
	"bytes"
	"database/sql"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	_ "github.com/lib/pq"
)

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

	// setup http server
	h1 := func(w http.ResponseWriter, req *http.Request) {
		key := req.URL.Path
		method := req.Method
		if method == "PUT" {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				log.Fatal(err)
			}
			req.Body.Close()

			buf := new(bytes.Buffer)
			if err := json.Compact(buf, []byte(body)); err != nil {
				fmt.Println(err)
			}

			val := b64.StdEncoding.EncodeToString(buf.Bytes())
			sql := fmt.Sprintf("select * from set('%s', '%s')", key, val)
			row := db.QueryRow(sql)
			var rev int64
			err = row.Scan(&rev)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Fprintf(w, "rev=%d\n", rev)
		} else if method == "DELETE" {
			sql := fmt.Sprintf("select * from del('%s')", key)
			row := db.QueryRow(sql)
			var rev int64
			err := row.Scan(&rev)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Fprintf(w, "rev=%d\n", rev)
		} else if method == "GET" {
			var rev int64
			revStr := req.URL.Query().Get("rev")
			if revStr != "" {
				var err error
				rev, err = strconv.ParseInt(revStr, 10, 64)
				if err != nil {
					panic(err)
				}
			}
			sql := fmt.Sprintf("select * from get('%s', %d)", key, rev)
			row := db.QueryRow(sql)
			var key string
			var value string
			var create_time int64
			err := row.Scan(&rev, &key, &value, &create_time)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Fprintf(w, "rev=%d, key=%s, value=%s, create_time=%d\n",
				rev, key, value, create_time)
		} else {
			w.WriteHeader(405)
			fmt.Fprintln(w, "method not allowed")
		}
	}

	http.HandleFunc("/", h1)

	listenAddr := ":9180"
	log.Printf("Start Control Plane, listen %s\n", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
