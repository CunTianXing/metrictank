package main

/*
 * Copyright (c) 2015, Raintank Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import (
	"encoding/json"
	"fmt"
	"github.com/ctdk/goas/v2/logger"
	"github.com/raintank/raintank-metric/eventdef"
	"github.com/raintank/raintank-metric/metricdef"
	"github.com/raintank/raintank-metric/metricstore"
	"github.com/raintank/raintank-metric/qproc"
	"github.com/raintank/raintank-metric/setting"
	"github.com/streadway/amqp"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

var metricDefs *metricdef.MetricDefCache

var bufCh chan metricdef.IndvMetric

func init() {
	setting.InitConfig()

	var numCPU int
	if setting.Config.NumWorkers != 0 {
		numCPU = setting.Config.NumWorkers
	} else {
		numCPU = runtime.NumCPU()
	}
	runtime.GOMAXPROCS(numCPU)

	bufCh = make(chan metricdef.IndvMetric, numCPU)

	err := eventdef.InitElasticsearch()
	if err != nil {
		panic(err)
	}
	err = metricdef.InitElasticsearch()
	if err != nil {
		panic(err)
	}
	err = metricdef.InitGroupcache()
	if err != nil {
		panic(err)
	}

	metricDefs, err = metricdef.InitMetricDefCache()
	if err != nil {
		panic(err)
	}

	for i := 0; i < numCPU; i++ {
		mStore, err := metricstore.NewMetricStore()
		if err != nil {
			panic(err)
		}

		go mStore.ProcessBuffer(bufCh, i)
	}
}

func main() {
	// First fire up a queue to consume metric def events
	mdConn, err := amqp.Dial(setting.Config.RabbitMQURL)
	if err != nil {
		logger.Criticalf(err.Error())
		os.Exit(1)
	}
	defer mdConn.Close()
	logger.Debugf("connected")

	done := make(chan error, 1)

	var numCPU int
	if setting.Config.NumWorkers != 0 {
		numCPU = setting.Config.NumWorkers
	} else {
		numCPU = runtime.NumCPU()
	}

	err = qproc.ProcessQueue(mdConn, "metrics", "topic", "", "metrics.*", false, true, true, done, processMetricDefEvent, numCPU)
	if err != nil {
		logger.Criticalf(err.Error())
		os.Exit(1)
	}
	err = qproc.ProcessQueue(mdConn, "metricResults", "x-consistent-hash", "", "10", false, true, true, done, processMetrics, numCPU)
	if err != nil {
		logger.Criticalf(err.Error())
		os.Exit(1)
	}
	err = initEventProcessing(mdConn, numCPU, done)
	if err != nil {
		logger.Criticalf(err.Error())
		os.Exit(1)
	}

	// Signal handling. If SIGQUIT is received, print out the current
	// stack. Otherwise if SIGINT or SIGTERM are received clean up and exit
	// in an orderly fashion.
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGQUIT, os.Interrupt, syscall.SIGTERM)
		buf := make([]byte, 1<<20)
		for sig := range sigs {
			if sig == syscall.SIGQUIT {
				// print out the current stack on SIGQUIT
				runtime.Stack(buf, true)
				log.Printf("=== received SIGQUIT ===\n*** goroutine dump...\n%s\n*** end\n", buf)
			} else {
				// finish our existing work, clean up, and exit
				// in an orderly fashion
				logger.Infof("Closing rabbitmq connection")
				cerr := mdConn.Close()
				if cerr != nil {
					logger.Errorf("Received error closing rabbitmq connection: %s", cerr.Error())
				}
				logger.Infof("Closing processing buffer channel")
				close(bufCh)
			}
		}
	}()

	// this channel returns when one of the workers exits.
	err = <-done
	logger.Criticalf("all done!", err)
	if err != nil {
		logger.Criticalf("Had an error, aiiieeee! '%s'", err.Error())
	}
}

func processMetrics(d *amqp.Delivery) error {
	metrics := make([]*metricdef.IndvMetric, 0)
	if err := json.Unmarshal(d.Body, &metrics); err != nil {
		return err
	}

	logger.Debugf("The parsed out json: %v", metrics)

	for _, m := range metrics {
		logger.Debugf("processing %s", m.Name)
		id := fmt.Sprintf("%d.%s", m.OrgId, m.Name)
		if m.Id == "" {
			m.Id = id
		}
		if err := metricDefs.CheckMetricDef(id, m); err != nil {
			return err
		}

		if err := storeMetric(m); err != nil {
			return err
		}
	}

	if err := d.Ack(false); err != nil {
		return err
	}
	return nil
}

func processMetricDefEvent(d *amqp.Delivery) error {
	action := strings.Split(d.RoutingKey, ".")[1]

	switch action {
	case "update":
		metric, err := metricdef.DefFromJSON(d.Body)
		if err != nil {
			return err
		}
		if err := metricDefs.UpdateDefCache(metric); err != nil {
			return err
		}
	case "remove":
		metric, err := metricdef.DefFromJSON(d.Body)
		if err != nil {
			return err
		}
		metricDefs.RemoveDefCache(metric.Id)
	default:
		err := fmt.Errorf("message has unknown action '%s'", action)
		return err
	}

	return nil
}

func storeMetric(met *metricdef.IndvMetric) error {
	logger.Debugf("storing metric: %+v", met)
	bufCh <- *met
	return nil
}
