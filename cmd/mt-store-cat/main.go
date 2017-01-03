package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/raintank/dur"
	"github.com/raintank/met/helper"
	"github.com/raintank/metrictank/api"
	"github.com/raintank/metrictank/iter"
	"github.com/raintank/metrictank/mdata"
	"github.com/rakyll/globalconf"
	"gopkg.in/raintank/schema.v1"
)

const tsFormat = "2006-01-02 15:04:05"

var (
	GitHash = "(none)"

	// flags from metrictank.go, globals
	showVersion = flag.Bool("version", false, "print version string")
	confFile    = flag.String("config", "/etc/raintank/metrictank.ini", "configuration file path")

	// flags from metrictank.go, Cassandra
	cassandraAddrs               = flag.String("cassandra-addrs", "localhost", "cassandra host (may be given multiple times as comma-separated list)")
	cassandraKeyspace            = flag.String("cassandra-keyspace", "raintank", "cassandra keyspace to use for storing the metric data table")
	cassandraConsistency         = flag.String("cassandra-consistency", "one", "write consistency (any|one|two|three|quorum|all|local_quorum|each_quorum|local_one")
	cassandraHostSelectionPolicy = flag.String("cassandra-host-selection-policy", "roundrobin", "")
	cassandraTimeout             = flag.Int("cassandra-timeout", 1000, "cassandra timeout in milliseconds")
	cassandraReadConcurrency     = flag.Int("cassandra-read-concurrency", 20, "max number of concurrent reads to cassandra.")
	//cassandraWriteConcurrency    = flag.Int("cassandra-write-concurrency", 10, "max number of concurrent writes to cassandra.")
	cassandraReadQueueSize = flag.Int("cassandra-read-queue-size", 100, "max number of outstanding reads before blocking. value doesn't matter much")
	//cassandraWriteQueueSize      = flag.Int("cassandra-write-queue-size", 100000, "write queue size per cassandra worker. should be large engough to hold all at least the total number of series expected, divided by how many workers you have")
	cassandraRetries   = flag.Int("cassandra-retries", 0, "how many times to retry a query before failing it")
	cqlProtocolVersion = flag.Int("cql-protocol-version", 4, "cql protocol version to use")

	cassandraSSL              = flag.Bool("cassandra-ssl", false, "enable SSL connection to cassandra")
	cassandraCaPath           = flag.String("cassandra-ca-path", "/etc/raintank/ca.pem", "cassandra CA certificate path when using SSL")
	cassandraHostVerification = flag.Bool("cassandra-host-verification", true, "host (hostname and server cert) verification when using SSL")

	cassandraAuth     = flag.Bool("cassandra-auth", false, "enable cassandra authentication")
	cassandraUsername = flag.String("cassandra-username", "cassandra", "username for authentication")
	cassandraPassword = flag.String("cassandra-password", "cassandra", "password for authentication")

	// our own flags
	from = flag.String("from", "-24h", "get data from (inclusive)")
	to   = flag.String("to", "now", "get data until (exclusive)")
	mdp  = flag.Int("mdp", 0, "max data points to return")
	fix  = flag.Int("fix", 0, "fix data to this interval like metrictank does quantization")
)

func printNormal(iters []iter.Iter, from, to uint32) {
	fmt.Println("number of chunks:", len(iters))
	for i, iter := range iters {
		fmt.Println("## chunk", i)
		for iter.Next() {
			ts, val := iter.Values()
			printRecord(ts, val, ts >= from && ts < to, math.IsNaN(val))
		}
	}
}

func printPointsNormal(points []schema.Point, from, to uint32) {
	fmt.Println("number of points:", len(points))
	for _, p := range points {
		printRecord(p.Ts, p.Val, p.Ts >= from && p.Ts < to, math.IsNaN(p.Val))
	}
}

func printRecord(ts uint32, val float64, in, nan bool) {
	if in {
		if nan {
			fmt.Println("> ", time.Unix(int64(ts), 0).Format(tsFormat), "NAN")
		} else {
			fmt.Println("> ", time.Unix(int64(ts), 0).Format(tsFormat), val)
		}
	} else {
		if nan {
			fmt.Println("- ", time.Unix(int64(ts), 0).Format(tsFormat), "NAN")
		} else {
			fmt.Println("- ", time.Unix(int64(ts), 0).Format(tsFormat), val)
		}
	}
}

func printSummary(iters []iter.Iter, from, to uint32) {

	var count int
	first := true
	var prevIn, prevNaN bool
	var ts uint32
	var val float64

	var followup = func(count int, in, nan bool) {
		fmt.Printf("... and %d more of in_range=%t nan=%t ...\n", count, in, nan)
	}

	for _, iter := range iters {
		for iter.Next() {
			ts, val = iter.Values()

			nan := math.IsNaN(val)
			in := (ts >= from && ts < to)

			if first {
				printRecord(ts, val, in, nan)
			} else if nan == prevNaN && in == prevIn {
				count++
			} else {
				followup(count, prevIn, prevNaN)
				printRecord(ts, val, in, nan)
				count = 0
			}

			prevNaN = nan
			prevIn = in
			first = false
		}
	}
	if count > 0 {
		followup(count, prevIn, prevNaN)
		fmt.Println("last value was:")
		printRecord(ts, val, prevIn, prevNaN)
	}
}

func printPointsSummary(points []schema.Point, from, to uint32) {

	var count int
	first := true
	var prevIn, prevNaN bool
	var ts uint32
	var val float64

	var followup = func(count int, in, nan bool) {
		fmt.Printf("... and %d more of in_range=%t nan=%t ...\n", count, in, nan)
	}

	for _, p := range points {
		ts, val = p.Ts, p.Val

		nan := math.IsNaN(val)
		in := (ts >= from && ts < to)

		if first {
			printRecord(ts, val, in, nan)
		} else if nan == prevNaN && in == prevIn {
			count++
		} else {
			followup(count, prevIn, prevNaN)
			printRecord(ts, val, in, nan)
			count = 0
		}

		prevNaN = nan
		prevIn = in
		first = false
	}
	if count > 0 {
		followup(count, prevIn, prevNaN)
		fmt.Println("last value was:")
		printRecord(ts, val, prevIn, prevNaN)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Println(os.Args[0])
		fmt.Println("Usage:")
		fmt.Printf("	mt-store-cat [flags] <normal|summary> id <metric-id>\n")
		fmt.Printf("	mt-store-cat [flags] <normal|summary> query <org-id> <graphite query> (not supported yet)\n")
		fmt.Println("Flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 3 {
		flag.Usage()
		os.Exit(-1)
	}

	selector := flag.Arg(1)
	var id string
	// var query string
	// var org int

	switch selector {
	case "id":
		id = flag.Arg(2)
	case "query":
		//		if flag.NArg() < 4 {
		//			flag.Usage()
		//			os.Exit(-1)
		//		}
		//		org64, err := strconv.ParseInt(flag.Arg(3), 10, 32)
		//		if err != nil {
		//			flag.Usage()
		//			os.Exit(-1)
		//		}
		//		org = int(org64)
		//		query = flag.Arg(4)
		panic("sorry, queries not supported yet")
	default:
		flag.Usage()
		os.Exit(-1)
	}

	now := time.Now()

	defaultFrom := uint32(now.Add(-time.Duration(24) * time.Hour).Unix())
	defaultTo := uint32(now.Add(time.Duration(1) * time.Second).Unix())

	fromUnix, err := dur.ParseTSpec(*from, now, defaultFrom)
	if err != nil {
		log.Fatal(err)
	}

	toUnix, err := dur.ParseTSpec(*to, now, defaultTo)
	if err != nil {
		log.Fatal(err)
	}

	// Only try and parse the conf file if it exists
	path := ""
	if _, err := os.Stat(*confFile); err == nil {
		path = *confFile
	}
	conf, err := globalconf.NewWithOptions(&globalconf.Options{
		Filename:  path,
		EnvPrefix: "MT_",
	})
	if err != nil {
		log.Fatal(4, "error with configuration file: %s", err)
		os.Exit(1)
	}

	conf.ParseAll()

	if *showVersion {
		fmt.Printf("mt-store-cat (built with %s, git hash %s)\n", runtime.Version(), GitHash)
		return
	}

	stats, err := helper.New(false, "", "standard", "mt-store-cat", "default")

	store, err := mdata.NewCassandraStore(stats, *cassandraAddrs, *cassandraKeyspace, *cassandraConsistency, *cassandraCaPath, *cassandraUsername, *cassandraPassword, *cassandraHostSelectionPolicy, *cassandraTimeout, *cassandraReadConcurrency, *cassandraReadConcurrency, *cassandraReadQueueSize, 0, *cassandraRetries, *cqlProtocolVersion, *cassandraSSL, *cassandraAuth, *cassandraHostVerification)
	if err != nil {
		log.Fatal(4, "failed to initialize cassandra. %s", err)
	}
	store.InitMetrics(stats)

	// if we're gonna mimic MT, then it would be:
	/*
		target, consolidateBy, err := parseTarget(query)
		consolidator, err := consolidation.GetConsolidator(&def, parsedTargets[target])
		if err != nil {
		}
		query := strings.Replace(queryForTarget[target], target, def.Name, -1)
		reqs = append(reqs, models.NewReq(def.Id, query, fromUnix, toUnix, request.MaxDataPoints, uint32(def.Interval), consolidator))
		reqs, err = alignRequests(reqs, s.MemoryStore.AggSettings())
		points, interval, err := s.getTarget(req)
		// ...
		merged := mergeSeries(out)
	*/

	mode := flag.Arg(0)

	if *fix != 0 {
		points := getSeries(id, fromUnix, toUnix, uint32(*fix), store)

		switch mode {
		case "normal":
			printPointsNormal(points, fromUnix, toUnix)
		case "summary":
			printPointsSummary(points, fromUnix, toUnix)
		default:
			panic("unsupported mode")
		}
	} else {

		iters, err := store.Search(id, fromUnix, toUnix)
		if err != nil {
			panic(err)
		}

		switch mode {
		case "normal":
			printNormal(iters, fromUnix, toUnix)
		case "summary":
			printSummary(iters, fromUnix, toUnix)
		default:
			panic("unsupported mode")
		}
	}

}

func getSeries(id string, fromUnix, toUnix, interval uint32, store mdata.Store) []schema.Point {
	iters, err := store.Search(id, fromUnix, toUnix)
	if err != nil {
		panic(err)
	}

	var points []schema.Point

	for _, iter := range iters {
		for iter.Next() {
			ts, val := iter.Values()
			if ts >= fromUnix && ts < toUnix {
				points = append(points, schema.Point{Val: val, Ts: ts})
			}
		}
	}
	return api.Fix(points, fromUnix, toUnix, interval)
}
