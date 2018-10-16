package consumer

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/Shopify/sarama"
	cluster "github.com/bsm/sarama-cluster"
	"github.com/heetch/felice/consumer/handler"
	"github.com/heetch/felice/message"
	"github.com/pkg/errors"
)

// Consumer is a Kafka consumer.
type Consumer struct {
	consumer      *cluster.Consumer
	config        *cluster.Config
	handlers      *handler.Collection
	wg            sync.WaitGroup
	quit          chan struct{}
	RetryInterval time.Duration
	metrics       MetricsHook
}

// Handle registers the handler for the given topic.
// Handle must be called before Serve.
func (c *Consumer) Handle(topic string, h handler.Handler) {
	c.setup()

	c.handlers.Set(topic, h)
}

func (c *Consumer) setup() {
	if c.handlers == nil {
		c.handlers = &handler.Collection{}
	}

	if c.quit == nil {
		c.quit = make(chan struct{})
	}

	if c.RetryInterval == 0 {
		c.RetryInterval = time.Second
	}
}

// SetMetricsHook registers a MetricsHook on the Consumer that will be invoked with datas about the handled messages.
func (c *Consumer) SetMetricsHook(mh MetricsHook) {
	c.metrics = mh
}

// Serve runs the consumer and listens for new messages on the given topics.
func (c *Consumer) Serve(clientID string, addrs ...string) error {
	c.setup()

	config := cluster.NewConfig()
	config.ClientID = clientID
	config.Consumer.Return.Errors = true
	// Specify that we are using at least Kafka v1.0
	config.Version = sarama.V1_0_0_0
	// Distribute load across instances using round robin strategy
	config.Group.PartitionStrategy = cluster.StrategyRoundRobin
	// One chan per partition instead of default multiplexing behaviour.
	config.Group.Mode = cluster.ConsumerModePartitions
	c.config = config
	topics := c.handlers.Topics()

	consumerGroup := fmt.Sprintf("%s-consumer-group", clientID)
	var err error
	c.consumer, err = cluster.NewConsumer(
		addrs,
		consumerGroup,
		topics,
		config)
	if err != nil {
		// Note: this kind of error comparison is weird, but
		// it's possible because sarama defines the KError
		// type as an int16 that supports the error interface.
		if kerr, ok := err.(sarama.KError); ok && kerr == sarama.ErrConsumerCoordinatorNotAvailable {
			// We'll add some additional context
			// information.  This issue comes from the
			// RefreshCoordinator call inside sarama, but
			// it's not reporting the root cause, sadly.
			//
			// Even with this information it's rather an
			// annoying little issue.
			err = errors.Wrap(err, "__consumer_offsets topic doesn't yet exist, either because no client has yet requested an offset, or because this consumer group is not yet functioning at startup or after rebalancing.")
		}
		return errors.Wrap(err, fmt.Sprintf("failed to create a consumer for topics %+v in consumer group %q", topics, consumerGroup))
	}

	err = c.handlePartitions(c.consumer.Partitions())
	return err
}

func (c *Consumer) handlePartitions(ch <-chan cluster.PartitionConsumer) error {
	for {
		select {
		case part, ok := <-ch:
			if !ok {
				return fmt.Errorf("partition consumer channel closed")
			}

			c.wg.Add(1)
			go func(pc cluster.PartitionConsumer) {
				defer c.wg.Done()

				c.handleMessages(pc.Messages(), c.consumer, pc)
			}(part)
		case <-c.quit:
			return nil
		}
	}
}

func (c *Consumer) handleMessages(ch <-chan *sarama.ConsumerMessage, offset offsetStash, max highWaterMarker) {
	for msg := range ch {
		var attempts int

		for {
			attempts++

			m := c.convertMessage(msg)
			// Note: The second returned value is not checked because we will never receive messages for a topic
			// that it does not have a handler for.
			h, _ := c.handlers.Get(msg.Topic)
			err := h.(handler.Handler).HandleMessage(m)
			if err == nil {
				offset.MarkOffset(msg, "")
				if c.metrics != nil {
					c.metrics.Reports(*m, map[string]string{
						"attempts":        strconv.Itoa(attempts),
						"msgOffset":       strconv.FormatInt(msg.Offset, 10),
						"remainingOffset": strconv.FormatInt(max.HighWaterMarkOffset()-msg.Offset, 10),
					})
				}
				break
			}

			select {
			case <-time.After(c.RetryInterval):
			case <-c.quit:
				return
			}
		}
	}
}

func (c *Consumer) convertMessage(cm *sarama.ConsumerMessage) *message.Message {
	var msg message.Message
	if cm.Key != nil {
		msg.Key = string(cm.Key)
	}

	msg.Topic = cm.Topic
	msg.ProducedAt = cm.Timestamp
	msg.Offset = cm.Offset
	msg.Partition = cm.Partition
	msg.Body = cm.Value

	return &msg
}

// MetricsHook is a interface that can be passed to set metrics hook to receive metrics
// from the consumer as it handles messages.
type MetricsHook interface {
	Reports(message.Message, map[string]string)
}

type offsetStash interface {
	MarkOffset(msg *sarama.ConsumerMessage, metadata string)
}

type highWaterMarker interface {
	HighWaterMarkOffset() int64
}
