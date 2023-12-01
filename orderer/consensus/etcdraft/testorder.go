package etcdraft

import (
	"fmt"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric/orderer/common/multichannel"
)

func (c *Chain) TestMultiClient() {
	time.Sleep(15 * time.Second)
	c.logger.Infof("HHHHHH starting the test timer HHHHHH")
	// c.start = time.Now()
	
	if c.raftID == 2 {
		wg := new(sync.WaitGroup)
		wg.Add(4)
		multichannel.SetStartTimer()
		go c.TestOrderClient1(wg)
		go c.TestOrderClient2(wg)
		go c.TestOrderClient3(wg)
		go c.TestOrderClient4(wg)
		wg.Wait()

	}

	// end := time.Now()

	// diff := end.Sub(start)
	// c.logger.Infof("HHHHHH The total time of execution is: %v with TPS: %f HHHHHH", diff, float64(400*math.Pow(10, 9)) / float64(diff))
}

func (c *Chain) TestOrderClient1(wg *sync.WaitGroup) {
	// time.Sleep(10000 * time.Millisecond)
	c.logger.Infof("For client %v", 1)
	for i := 0; i < 250; i++ {
		env := &common.Envelope{
			Payload: marshalOrPanic(&common.Payload{
				Header: &common.Header{ChannelHeader: marshalOrPanic(&common.ChannelHeader{Type: int32(common.HeaderType_MESSAGE), ChannelId: c.channelID})},
				Data:   []byte(fmt.Sprintf("TEST_MESSAGE-UNCC-Client-1-%v", i)),
			}),
		}

		c.Order(env, 0)
		c.logger.Infof("Message from Client - 1 - %v Enqueued", i+1)
		// time.Sleep(1 * time.Second)
	}
	wg.Done()
}

// this test will run after 20 second for network healthchck after TCP IO error being generated
func (c *Chain) TestOrderClient2(wg *sync.WaitGroup) {
	// time.Sleep(20000 * time.Millisecond)
	c.logger.Infof("For client %v", 2)
	for i := 0; i < 250; i++ {
		env := &common.Envelope{
			Payload: marshalOrPanic(&common.Payload{
				Header: &common.Header{ChannelHeader: marshalOrPanic(&common.ChannelHeader{Type: int32(common.HeaderType_MESSAGE), ChannelId: c.channelID})},
				Data:   []byte(fmt.Sprintf("TEST_MESSAGE-UNCC-Client-2-%v", i)),
			}),
		}

		c.Order(env, 0)
		c.logger.Infof("Message from Client - 2 - %v Enqueued", i+1)
		// time.Sleep(1 * time.Second)
	}
	wg.Done()
}

// this test will run after 20 second for network healthchck after TCP IO error being generated
func (c *Chain) TestOrderClient3(wg *sync.WaitGroup) {
	// time.Sleep(20000 * time.Millisecond)
	c.logger.Infof("For client %v", 3)
	for i := 0; i < 250; i++ {
		env := &common.Envelope{
			Payload: marshalOrPanic(&common.Payload{
				Header: &common.Header{ChannelHeader: marshalOrPanic(&common.ChannelHeader{Type: int32(common.HeaderType_MESSAGE), ChannelId: c.channelID})},
				Data:   []byte(fmt.Sprintf("TEST_MESSAGE-UNCC-Client-3-%v", i)),
			}),
		}

		c.Order(env, 0)
		c.logger.Infof("Message from Client - 3 - %v Enqueued", i+1)
		// time.Sleep(1 * time.Second)
	}
	wg.Done()
}

// this test will run after 20 second for network healthchck after TCP IO error being generated
func (c *Chain) TestOrderClient4(wg *sync.WaitGroup) {
	// time.Sleep(20000 * time.Millisecond)
	c.logger.Infof("For client %v", 3)
	// c.logger.Infof("After calling c.Order(env, 0) ")
	for i := 0; i < 250; i++ {
		env := &common.Envelope{
			Payload: marshalOrPanic(&common.Payload{
				Header: &common.Header{ChannelHeader: marshalOrPanic(&common.ChannelHeader{Type: int32(common.HeaderType_MESSAGE), ChannelId: c.channelID})},
				Data:   []byte(fmt.Sprintf("TEST_MESSAGE-UNCC-Client-4-%v", i)),
			}),
		}

		c.Order(env, 0)
		c.logger.Infof("Message from Client - 4 - %v Enqueued", i+1)
		// time.Sleep(1 * time.Second)
	}
	wg.Done()
}

func marshalOrPanic(pb proto.Message) []byte {
	data, err := proto.Marshal(pb)
	if err != nil {
		panic(err)
	}
	return data
}
