package kafka

import (
	"github.com/grafana/grafana/pkg/log"
	"sync"

	"github.com/bsm/sarama-cluster"
	"github.com/raintank/met"
	"github.com/raintank/raintank-metric/metric_tank/defcache"
	"github.com/raintank/raintank-metric/metric_tank/in"
	"github.com/raintank/raintank-metric/metric_tank/mdata"
	"github.com/raintank/raintank-metric/metric_tank/usage"
)

type Kafka struct {
	in.In
	consumer *cluster.Consumer
	stats    met.Backend

	wg sync.WaitGroup
	// read from this channel to block until consumer is cleanly stopped
	StopChan chan int
}

func New(broker, topic string, stats met.Backend) *Kafka {
	brokers := []string{broker}
	groupId := "group1"
	topics := []string{topic}

	config := cluster.NewConfig()
	//config.Consumer.Offsets.Initial = sarama.OffsetOldest
	config.Group.Return.Notifications = true
	err := config.Validate()
	if err != nil {
		log.Fatal(2, "kafka invalid config: %s", err)
	}
	consumer, err := cluster.NewConsumer(brokers, groupId, topics, config)
	if err != nil {
		log.Fatal(2, "kafka failed to start consumer: %s", err)
	}
	log.Info("kafka consumer started without error")
	k := Kafka{
		consumer: consumer,
		stats:    stats,
		StopChan: make(chan int),
	}

	return &k
}

func (k *Kafka) Start(metrics mdata.Metrics, defCache *defcache.DefCache, usg *usage.Usage) {
	k.In = in.New(metrics, defCache, usg, "kafka", k.stats)
	go k.notifications()
	go k.consume()
}

func (k *Kafka) consume() {
	k.wg.Add(1)
	messageChan := k.consumer.Messages()
	for msg := range messageChan {
		log.Debug("kafka received message: Topic %s, Partition: %d, Offset: %d, Key: %s", msg.Topic, msg.Partition, msg.Offset, string(msg.Key))
		k.In.Handle(msg.Value)
		k.consumer.MarkOffset(msg, "")
	}
	log.Info("kafka consumer ended.")
	k.wg.Done()
}

func (k *Kafka) notifications() {
	k.wg.Add(1)
	for msg := range k.consumer.Notifications() {
		if len(msg.Claimed) > 0 {
			for topic, partitions := range msg.Claimed {
				log.Info("consumer claimed %d partitions on topic: %s", len(partitions), topic)
			}
		}
		if len(msg.Released) > 0 {
			for topic, partitions := range msg.Released {
				log.Info("consumer released %d partitions on topic: %s", len(partitions), topic)
			}
		}

		if len(msg.Current) == 0 {
			log.Info("consumer is no longer consuming from any partitions.")
		} else {
			log.Info("Current partitions:")
			for topic, partitions := range msg.Current {
				log.Info("Current partitions: %s: %v", topic, partitions)
			}
		}
	}
	log.Info("kafka notification processing stopped")
	k.wg.Done()
}

// Stop will initiate a graceful stop of the Consumer (permanent)
//
// NOTE: receive on StopChan to block until this process completes
func (k *Kafka) Stop() {
	// closes notifications and messages channels, amongst others
	k.consumer.Close()

	go func() {
		k.wg.Wait()
		close(k.StopChan)
	}()
}
