# pg_watch_demo

With trigger and notify, you could re-implement an complete (even better) etcd watch mechanism in postgresql.

It mimics below etcd features:

* watch (*)
* read value in historical data, i.e. get key by revision
* del key
* compact, either by revision or date retention

*\* You could watch specific resources by prefix (e.g. `routes`, `upstreams`, etc.), just like what etcd does.*

## Demo

This demo consist of three parts:

* `config.sql` script to setup table and trigger
* `dp.go` data plane to act as a simple reverse proxy based on routes rules
* `cp.go` control plane to manipulate routes rules

## Setup

```bash
cd /opt
git clone https://github.com/kingluo/pg_watch_demo
cd pg_watch_demo

mkdir /opt/pg_data1
docker run -d --rm --name postgres -p 5432:5432 -v /opt/pg_data1:/var/lib/postgresql/data -e POSTGRES_PASSWORD=postgres -e POSTGRES_HOST_AUTH_METHOD=md5 -d postgres:14

docker cp ./config.sql postgres:/tmp/
docker exec postgres psql -h localhost -d postgres -U postgres -q -f /tmp/config.sql

# run data plane in one terminal
go run dp.go

# run control plane in another terminal
go run cp.go
```

## Test

```bash
# no routes initially

curl -i http://localhost:9080/get

HTTP/1.1 404 Not Found
Date: Sun, 04 Sep 2022 03:04:03 GMT
Content-Length: 9
Content-Type: text/plain; charset=utf-8

no route


# setup a route for `/get`

curl -X PUT http://localhost:9180/routes/foo -d '
{
	"uri": "/get",
	"upstream": "http://httpbin.org"
}
'

rev=8

# reverse proxy works

curl -i http://localhost:9080/get

HTTP/1.1 200 OK
Access-Control-Allow-Credentials: true
Access-Control-Allow-Origin: *
Connection: keep-alive
Content-Length: 270
Content-Type: application/json
Date: Sun, 04 Sep 2022 03:04:44 GMT
Server: gunicorn/19.9.0

{
  "args": {},
  "headers": {
    "Accept-Encoding": "gzip",
    "Host": "httpbin.org",
    "User-Agent": "Go-http-client/1.1",
    "X-Amzn-Trace-Id": "Root=1-631415cc-09d261d55b5c5b5f096f2cbf"
  },
  "origin": "xxx",
  "url": "http://httpbin.org/get"
}


# setup a route for `/anything`

curl -X PUT http://localhost:9180/routes/bar -d '
{
	"uri": "/anything",
	"upstream": "http://httpbin.org"
}
'

rev=9

# reverse proxy works

curl -i http://localhost:9080/anything

HTTP/1.1 200 OK
Access-Control-Allow-Credentials: true
Access-Control-Allow-Origin: *
Connection: keep-alive
Content-Length: 358
Content-Type: application/json
Date: Sun, 04 Sep 2022 03:05:52 GMT
Server: gunicorn/19.9.0

{
  "args": {},
  "data": "",
  "files": {},
  "form": {},
  "headers": {
    "Accept-Encoding": "gzip",
    "Host": "httpbin.org",
    "User-Agent": "Go-http-client/1.1",
    "X-Amzn-Trace-Id": "Root=1-63141610-27af9f8d0bae360f2b727253"
  },
  "json": null,
  "method": "GET",
  "origin": "xxx",
  "url": "http://httpbin.org/anything"
}


# override a route for `/get`

curl -X PUT http://localhost:9180/routes/foo -d '
{
	"uri": "/get",
	"upstream": "http://httpbin.org"
}
'

rev=10

# demo how to get current version and history version

curl -X GET http://localhost:9180/routes/foo

rev=10, key=/routes/foo, value=eyJ1cmkiOiIvZ2V0IiwidXBzdHJlYW0iOiJodHRwOi8vaHR0cGJpbi5vcmcifQ==, create_time=1662260776764

curl -X GET http://localhost:9180/routes/foo?rev=8

rev=8, key=/routes/foo, value=eyJ1cmkiOiIvZ2V0IiwidXBzdHJlYW0iOiJodHRwOi8vaHR0cGJpbi5vcmcifQ==, create_time=1662260665556

# delete a route and check again

curl -X DELETE http://localhost:9180/routes/bar

rev=11

curl -i http://localhost:9080/anything

HTTP/1.1 404 Not Found
Date: Sun, 04 Sep 2022 03:07:40 GMT
Content-Length: 9
Content-Type: text/plain; charset=utf-8

no route


# dp log

2022/09/04 11:00:50 get all routes...
2022/09/04 11:00:50 Start watching...
2022/09/04 11:00:50 Start Data Plane, listen :9080
2022/09/04 11:02:20 Received no events for 90 seconds, checking connection
2022/09/04 11:03:50 Received no events for 90 seconds, checking connection
2022/09/04 11:04:25 receive route notification: channel=routes, watch_delay=3 milliseconds: route: {"key":"/routes/foo","value":"eyJ1cmkiOiIvZ2V0IiwidXBzdHJlYW0iOiJodHRwOi8vaHR0cGJpbi5vcmcifQ==","revision":8,"tombstone":false,"create_time":1662260665556}
2022/09/04 11:04:25 add route: {"uri":"/get","upstream":"http://httpbin.org"}
2022/09/04 11:04:44 /get -> http://httpbin.org
2022/09/04 11:05:28 receive route notification: channel=routes, watch_delay=2 milliseconds: route: {"key":"/routes/bar","value":"eyJ1cmkiOiIvYW55dGhpbmciLCJ1cHN0cmVhbSI6Imh0dHA6Ly9odHRwYmluLm9yZyJ9","revision":9,"tombstone":false,"create_time":1662260728289}
2022/09/04 11:05:28 add route: {"uri":"/anything","upstream":"http://httpbin.org"}
2022/09/04 11:05:52 /anything -> http://httpbin.org
2022/09/04 11:06:16 receive route notification: channel=routes, watch_delay=2 milliseconds: route: {"key":"/routes/foo","value":"eyJ1cmkiOiIvZ2V0IiwidXBzdHJlYW0iOiJodHRwOi8vaHR0cGJpbi5vcmcifQ==","revision":10,"tombstone":false,"create_time":1662260776764}
2022/09/04 11:06:16 add route: {"uri":"/get","upstream":"http://httpbin.org"}
2022/09/04 11:07:29 receive route notification: channel=routes, watch_delay=2 milliseconds: route: {"key":"/routes/bar","value":null,"revision":11,"tombstone":true,"create_time":1662260849783}
2022/09/04 11:07:29 del route: {"uri":"/anything","upstream":"http://httpbin.org"}

```

**Note that `watch_delay` is 2 or 3 milliseconds, it's fast to sync the routes changes between postgresql and dp!**

## compact

```sql
-- delete items older before revision 7
delete from config where revision < 7;

-- delete items older than specific date
delete from config where create_time < (EXTRACT(EPOCH FROM TIMESTAMP '2011-05-17 10:40:28.876944') * 1000)::bigint;
```
