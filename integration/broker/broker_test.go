package broker

import (
	"context"
	"log"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/celrenheit/sandglass/sgutils"

	"github.com/celrenheit/sandglass/topic"

	"fmt"

	"io/ioutil"

	"os"

	"github.com/celrenheit/sandflake"
	"github.com/celrenheit/sandglass/broker"
	"github.com/celrenheit/sandglass/logy"
	"github.com/celrenheit/sandglass/server"
	"github.com/celrenheit/sandglass/sgproto"
	"github.com/docker/libkv"
	"github.com/docker/libkv/store"
	"github.com/stretchr/testify/require"
)

func TestLeak(t *testing.T) {
	// defer leaktest.CheckTimeout(t, 15*time.Second)()
	n := 1
	_, destroyFn := makeNBrokers(t, n)
	destroyFn()
}

func TestSandglass(t *testing.T) {
	store, err := libkv.NewStore(store.ETCD, []string{sgutils.TestETCDAddr()}, nil)
	require.Nil(t, err)
	defer store.Close()
	cleanStore(t, store)
	time.Sleep(500 * time.Millisecond)
	n := 3
	brokers, destroyFn := makeNBrokers(t, n)
	defer destroyFn()

	createTopicParams := &sgproto.CreateTopicParams{
		Name:              "payments",
		Kind:              sgproto.TopicKind_TimerKind,
		ReplicationFactor: 2,
		NumPartitions:     3,
	}
	err = brokers[0].CreateTopic(createTopicParams)
	require.Nil(t, err)

	err = brokers[0].CreateTopic(createTopicParams)
	require.NotNil(t, err)

	// waiting for goroutine to receive topic
	time.Sleep(2000 * time.Millisecond)
	require.Len(t, brokers[0].Members(), n)
	for i := 0; i < n; i++ {
		require.Len(t, brokers[i].Topics(), 2)
	}

	time.Sleep(2000 * time.Millisecond)
	for i := 0; i < 1000; i++ {
		_, err := brokers[0].PublishMessage(&sgproto.Message{
			Topic: "payments",
			Value: []byte(strconv.Itoa(i)),
		})
		require.Nil(t, err)
	}

	var count int
	err = brokers[0].FetchRange("payments", "", sandflake.Nil, sandflake.MaxID, func(keymsg *sgproto.Message) error {
		count++
		return nil
	})
	require.Nil(t, err)

	require.Equal(t, 1000, count)
}

func TestCompactedTopic(t *testing.T) {
	store, err := libkv.NewStore(store.ETCD, []string{sgutils.TestETCDAddr()}, nil)
	require.Nil(t, err)
	defer store.Close()
	cleanStore(t, store)
	time.Sleep(500 * time.Millisecond)
	n := 3
	brokers, destroyFn := makeNBrokers(t, n)
	defer destroyFn()

	createTopicParams := &sgproto.CreateTopicParams{
		Name:              "payments",
		Kind:              sgproto.TopicKind_CompactedKind,
		ReplicationFactor: 2,
		NumPartitions:     3,
	}
	err = brokers[0].CreateTopic(createTopicParams)
	require.Nil(t, err)

	err = brokers[0].CreateTopic(createTopicParams)
	require.NotNil(t, err)

	// waiting for goroutine to receive topic
	time.Sleep(5000 * time.Millisecond)
	require.Len(t, brokers[0].Members(), n)
	for i := 0; i < n; i++ {
		require.Len(t, brokers[i].Topics(), 2)
	}

	// time.Sleep(2000 * time.Millisecond)
	for i := 0; i < 1000; i++ {
		_, err := brokers[0].PublishMessage(&sgproto.Message{
			Topic: "payments",
			Key:   []byte("my_key"),
			Value: []byte(strconv.Itoa(i)),
		})
		require.Nil(t, err)
	}

	var count int
	err = brokers[0].FetchRange("payments", "", sandflake.Nil, sandflake.MaxID, func(msg *sgproto.Message) error {
		require.Equal(t, "my_key", string(msg.Key))
		count++
		return nil
	})
	require.Nil(t, err)

	require.Equal(t, 1, count)

	msg, err := brokers[0].Get("payments", "", []byte("my_key"))
	require.NoError(t, err)
	require.Equal(t, "999", string(msg.Value))
}

func TestACK(t *testing.T) {
	store, err := libkv.NewStore(store.ETCD, []string{sgutils.TestETCDAddr()}, nil)
	require.Nil(t, err)
	defer store.Close()
	cleanStore(t, store)
	time.Sleep(500 * time.Millisecond)
	n := 3
	brokers, destroyFn := makeNBrokers(t, n)
	defer destroyFn()

	createTopicParams := &sgproto.CreateTopicParams{
		Name:              "payments",
		Kind:              sgproto.TopicKind_TimerKind,
		ReplicationFactor: 2,
		NumPartitions:     3,
	}
	err = brokers[0].CreateTopic(createTopicParams)
	require.Nil(t, err)

	err = brokers[0].CreateTopic(createTopicParams)
	require.NotNil(t, err)

	// waiting for goroutine to receive topic
	time.Sleep(4000 * time.Millisecond)
	require.Len(t, brokers[0].Members(), n)
	for i := 0; i < n; i++ {
		require.Len(t, brokers[i].Topics(), 2)
	}

	b := brokers[2]
	var topic *topic.Topic
	for _, t := range brokers[0].Topics() {
		if t.Name == createTopicParams.Name {
			topic = t
		}
	}

	var g sandflake.Generator
	offset := g.Next()
	ok, err := b.Acknowledge(topic.Name, topic.Partitions[0].Id, "group1", "cons1", offset)
	require.Nil(t, err)
	require.True(t, ok)

	got, err := b.LastOffset(topic.Name, topic.Partitions[0].Id, "group1", "cons1",
		sgproto.LastOffsetRequest_Commited)
	require.Nil(t, err)
	require.Equal(t, sandflake.Nil, got)

	got, err = b.LastOffset(topic.Name, topic.Partitions[0].Id, "group1", "cons1",
		sgproto.LastOffsetRequest_Acknowledged)
	require.Nil(t, err)
	require.Equal(t, offset, got)

	offset2 := g.Next()
	ok, err = b.Commit(topic.Name, topic.Partitions[0].Id, "group1", "cons1", offset2)
	require.Nil(t, err)
	require.True(t, ok)

	got, err = b.LastOffset(topic.Name, topic.Partitions[0].Id, "group1", "cons1",
		sgproto.LastOffsetRequest_Commited)
	require.Nil(t, err)
	require.Equal(t, offset2, got)

	got, err = b.LastOffset(topic.Name, topic.Partitions[0].Id, "group1", "cons1",
		sgproto.LastOffsetRequest_Acknowledged)
	require.Nil(t, err)
	require.Equal(t, offset, got)
}

func TestConsume(t *testing.T) {
	store, err := libkv.NewStore(store.ETCD, []string{sgutils.TestETCDAddr()}, nil)
	require.Nil(t, err)
	defer store.Close()
	cleanStore(t, store)
	time.Sleep(500 * time.Millisecond)
	n := 3
	brokers, destroyFn := makeNBrokers(t, n)
	defer destroyFn()

	createTopicParams := &sgproto.CreateTopicParams{
		Name:              "payments",
		Kind:              sgproto.TopicKind_TimerKind,
		ReplicationFactor: 2,
		NumPartitions:     3,
	}
	err = brokers[0].CreateTopic(createTopicParams)
	require.Nil(t, err)

	err = brokers[0].CreateTopic(createTopicParams)
	require.NotNil(t, err)

	// waiting for goroutine to receive topic
	time.Sleep(5000 * time.Millisecond)
	require.Len(t, brokers[0].Members(), n)
	for i := 0; i < n; i++ {
		require.Len(t, brokers[i].Topics(), 2)
	}

	b := brokers[2]
	var topic *topic.Topic
	for _, t := range brokers[0].Topics() {
		if t.Name == createTopicParams.Name {
			topic = t
		}
	}

	var gen sandflake.Generator
	var want sandflake.ID
	var ids []sandflake.ID
	for i := 0; i < 30; i++ {
		want = gen.Next()
		_, err := brokers[0].PublishMessage(&sgproto.Message{
			Topic:     "payments",
			Offset:    want,
			Partition: topic.Partitions[0].Id,
			Value:     []byte(strconv.Itoa(i)),
		})
		require.Nil(t, err)
		ids = append(ids, want)
	}

	fmt.Println("-----------------------------")
	var count int
	var got sandflake.ID
	err = b.Consume("payments", topic.Partitions[0].Id, "group1", "cons1", func(msg *sgproto.Message) error {
		count++
		ok, err := b.Acknowledge(topic.Name, topic.Partitions[0].Id, "group1", "cons1", msg.Offset)
		require.True(t, ok)
		got = msg.Offset
		return err
	})
	require.Nil(t, err)
	require.Equal(t, 30, count)
	require.Equal(t, want, got)

	for i := 0; i < 20; i++ {
		res, err := brokers[0].PublishMessage(&sgproto.Message{
			Topic:     "payments",
			Partition: topic.Partitions[0].Id,
			Value:     []byte(strconv.Itoa(i)),
		})
		require.Nil(t, err)
		want = *res
	}

	fmt.Println("-----------------------------")
	count = 0
	err = b.Consume("payments", topic.Partitions[0].Id, "group1", "cons1", func(msg *sgproto.Message) error {
		count++
		got = msg.Offset
		return nil
	})
	require.Nil(t, err)
	require.Equal(t, 20, count)
	require.Equal(t, want, got)

	ok, err := b.Commit(topic.Name, topic.Partitions[0].Id, "group1", "cons1", got)
	require.True(t, ok)
	require.Nil(t, err)

	count = 0
	err = b.Consume("payments", topic.Partitions[0].Id, "group1", "cons1", func(msg *sgproto.Message) error {
		count++
		return nil
	})
	require.Nil(t, err)
	require.Equal(t, 0, count)
}

func TestSyncRequest(t *testing.T) {
	broker.DefaultStateCheckInterval = 300 * time.Second
	store, err := libkv.NewStore(store.ETCD, []string{sgutils.TestETCDAddr()}, nil)
	require.Nil(t, err)
	defer store.Close()
	cleanStore(t, store)
	time.Sleep(500 * time.Millisecond)
	n := 3
	brokers, destroyFn := makeNBrokers(t, n)
	defer destroyFn()

	createTopicParams := &sgproto.CreateTopicParams{
		Name:              "payments",
		Kind:              sgproto.TopicKind_TimerKind,
		ReplicationFactor: 2,
		NumPartitions:     3,
	}
	err = brokers[0].CreateTopic(createTopicParams)
	require.Nil(t, err)

	err = brokers[0].CreateTopic(createTopicParams)
	require.NotNil(t, err)

	// waiting for goroutine to receive topic
	time.Sleep(5000 * time.Millisecond)
	require.Len(t, brokers[0].Members(), n)
	for i := 0; i < n; i++ {
		require.Len(t, brokers[i].Topics(), 2)
	}

	topic := getTopicFromBroker(brokers[0], createTopicParams.Name)

	part := topic.Partitions[0]

	var gen sandflake.Generator
	var lastPublishedID sandflake.ID
	for i := 0; i < 5; i++ {
		lastPublishedID = gen.Next()
		_, err := brokers[0].PublishMessage(&sgproto.Message{
			Topic:     "payments",
			Partition: part.Id,
			Offset:    lastPublishedID,
			Value:     []byte(strconv.Itoa(i)),
		})
		require.Nil(t, err)
	}

	for _, b := range brokers {
		err := b.TriggerSyncRequest()
		require.NoError(t, err)
	}

	lastOffsets := make(map[string]sandflake.ID)
	for _, b := range brokers {
		if sgutils.StringSliceHasString(part.Replicas, b.Name()) {
			tt := getTopicFromBroker(b, createTopicParams.Name)
			p := tt.GetPartition(part.Id)
			lastMsg, err := p.LastMessage()
			require.NoError(t, err)
			var lastOffset sandflake.ID
			if lastMsg != nil {
				lastOffset = lastMsg.Offset
			}
			lastOffsets[b.Name()] = lastOffset
		}
	}
	fmt.Printf("lastPublishedID: %+v\n", lastPublishedID)
	fmt.Printf("replicas: %+v\n", part.Replicas)
	fmt.Printf("partition: %+v\n", part.Id)
	fmt.Printf("lastOffsets: %+v\n", lastOffsets)
	require.Len(t, lastOffsets, len(part.Replicas))
	for host, offset := range lastOffsets {
		require.Equal(t, lastPublishedID, offset, "host '%v' does not match", host)
	}
}

func getTopicFromBroker(b *broker.Broker, topic string) *topic.Topic {
	for _, t := range b.Topics() {
		if t.Name == topic {
			return t
		}
	}

	return nil
}

func BenchmarkCompactedTopicGet(b *testing.B) {
	store, err := libkv.NewStore(store.ETCD, []string{sgutils.TestETCDAddr()}, nil)
	require.Nil(b, err)
	defer store.Close()
	cleanStore(b, store)
	time.Sleep(500 * time.Millisecond)
	n := 3
	brokers, destroyFn := makeNBrokers(b, n)
	defer destroyFn()

	createTopicParams := &sgproto.CreateTopicParams{
		Name:              "payments",
		Kind:              sgproto.TopicKind_CompactedKind,
		ReplicationFactor: 2,
		NumPartitions:     3,
	}
	err = brokers[0].CreateTopic(createTopicParams)
	require.Nil(b, err)

	err = brokers[0].CreateTopic(createTopicParams)
	require.NotNil(b, err)

	// waiting for goroutine to receive topic
	time.Sleep(1000 * time.Millisecond)
	require.Len(b, brokers[0].Members(), n)
	for i := 0; i < n; i++ {
		require.Len(b, brokers[i].Topics(), 1)
	}

	// time.Sleep(2000 * time.Millisecond)
	for i := 0; i < 30; i++ {
		_, err := brokers[0].PublishMessage(&sgproto.Message{
			Topic: "payments",
			Key:   []byte("my_key"),
			Value: []byte(strconv.Itoa(i)),
		})
		require.Nil(b, err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			msg, err := brokers[0].Get("payments", "", []byte("my_key"))
			require.NoError(b, err)
			require.Equal(b, "29", string(msg.Value))
		}
	})
}

func makeNBrokers(tb testing.TB, n int) (brokers []*broker.Broker, destroyFn func()) {
	var g sandflake.Generator
	dc := g.Next()
	paths := []string{}
	for i := 0; i < n; i++ {
		basepath, err := ioutil.TempDir("", "")
		require.Nil(tb, err)
		paths = append(paths, basepath)
		advertise_addr := RandomAddr()
		grpc_addr := RandomAddr()
		http_addr := RandomAddr()
		brokers = append(brokers, newBroker(tb, i, dc.String(), advertise_addr, grpc_addr, http_addr, basepath))
	}

	for _, b := range brokers {
		err := b.WaitForIt()
		require.Nil(tb, err)
	}

	servers := []*server.Server{}
	var doneServers sync.WaitGroup
	for i := 0; i < n; i++ {
		grpc_addr := brokers[i].Conf().GRPCAddr
		http_addr := brokers[i].Conf().HTTPAddr

		server := server.New(brokers[i], grpc_addr, http_addr, logy.NewWithLogger(logger, logy.INFO))
		doneServers.Add(1)
		go func() {
			defer doneServers.Done()
			server.Start()
			// require.Nil(t, err)
		}()

		servers = append(servers, server)
	}

	peers := []string{}
	for _, b := range brokers[1:] {
		peers = append(peers, b.Conf().AdvertiseAddr)
		err := b.Join(brokers[0].Conf().AdvertiseAddr)
		require.Nil(tb, err)
	}

	err := brokers[0].Join(peers...)
	require.Nil(tb, err)

	destroyFn = func() {
		for _, b := range brokers {
			err := b.Stop(context.Background())
			require.Nil(tb, err)
		}
		for _, s := range servers {
			err := s.Shutdown(context.Background())
			require.Nil(tb, err)
		}
		doneServers.Wait()
		for _, p := range paths {
			os.RemoveAll(p)
		}
	}
	return
}

var logger = log.New(os.Stdout, "", log.LstdFlags)

func newBroker(tb testing.TB, i int, dc, adv_addr, grpc_addr, http_addr, basepath string) *broker.Broker {
	conf := &broker.Config{
		Name:             "broker" + strconv.Itoa(i),
		DCName:           dc,
		DiscoveryBackend: "etcd",
		DiscoveryAddrs:   []string{sgutils.TestETCDAddr()},
		AdvertiseAddr:    adv_addr,
		DBPath:           basepath,
		GRPCAddr:         grpc_addr,
		HTTPAddr:         http_addr,
	}
	fmt.Printf("conf: %+v\n", conf)
	fmt.Printf("basepath: %+v\n", basepath)

	b, err := broker.New(conf)
	require.Nil(tb, err)

	err = b.Bootstrap()
	require.Nil(tb, err)

	return b
}

func cleanStore(tb testing.TB, s store.Store) {
	err := s.DeleteTree(broker.ETCDBasePrefix)
	if err != store.ErrKeyNotFound {
		require.NoError(tb, err)
	}

	time.Sleep(200 * time.Millisecond)
}

func RandomAddr() string {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		panic(err)
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().String()
}