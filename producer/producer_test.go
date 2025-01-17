package producer_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Shopify/sarama/mocks"
	"github.com/heetch/felice/producer"
	"github.com/stretchr/testify/require"
)

func TestSendMessage(t *testing.T) {
	msgs := []*producer.Message{
		producer.NewMessage("topic", "message"),
		&producer.Message{Topic: "topic", Body: "message"},
	}

	for _, msg := range msgs {
		cfg := producer.NewConfig("id", producer.MessageConverterV1())
		msp := mocks.NewSyncProducer(t, &cfg.Config)

		p, err := producer.NewFrom(msp, cfg)
		require.NoError(t, err)

		msp.ExpectSendMessageWithCheckerFunctionAndSucceed(func(val []byte) error {
			exp := "\"message\""
			if string(val) != exp {
				return fmt.Errorf("expected: %s but got: %s", exp, val)
			}
			return nil
		})
		err = p.SendMessage(context.Background(), msg)
		require.NoError(t, err)

		msp.ExpectSendMessageAndFail(fmt.Errorf("cannot produce message"))
		err = p.SendMessage(context.Background(), msg)
		require.EqualError(t, err, "producer: failed to send message: cannot produce message")
	}
}

func TestSend(t *testing.T) {
	msp := mocks.NewSyncProducer(t, nil)
	cfg := producer.NewConfig("id", producer.MessageConverterV1())
	p, err := producer.NewFrom(msp, cfg)
	require.NoError(t, err)

	msp.ExpectSendMessageAndSucceed()
	msg, err := p.Send(context.Background(), "topic", "message", producer.Int64Key(10), producer.Header("k", "v"))
	require.NoError(t, err)
	key, err := msg.Key.Encode()
	require.NoError(t, err)

	require.EqualValues(t, "10", key)
	require.Equal(t, "v", msg.Headers["k"])

	msp.ExpectSendMessageAndFail(fmt.Errorf("cannot produce message"))
	_, err = p.Send(context.Background(), "topic", "message")
	require.EqualError(t, err, "producer: failed to send message: cannot produce message")
}
