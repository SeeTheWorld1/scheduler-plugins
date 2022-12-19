package latency

import (
	"sync"
	"time"

	"github.com/go-ping/ping"
	"k8s.io/klog/v2"
)

type nodeLatency struct {
	sync.RWMutex
	TimeMap map[string]time.Duration
}

var NodeLatency = &nodeLatency{
	TimeMap: make(map[string]time.Duration),
}

// func initNodeLatency() *nodeLatency {
// 	return &nodeLatency{
// 		TimeMap: make(map[string]time.Duration),
// 	}
// }

// get the avgRtt to a node
func GetNodeLatency(dst string) time.Duration {
	sumLatency := time.Second * 0
	for i := 0; i < 5; i++ {
		latency := DetectLatency(dst)
		if latency == 0 { //sometimes a node cannot access the endpoints of some services, for example, the ip of the dns of the k8s cluster. At that time, we set them in a equal latency
			klog.Fatalf("ping 0: %s", dst)
		}
		sumLatency += latency
	}
	latency := sumLatency / 5
	return latency
}

// get the avgRtt to a ip
func DetectLatency(dst string) time.Duration {
	pinger, err := ping.NewPinger(dst)
	if err != nil {
		klog.ErrorS(err, "new pinger wrong!")
		panic(err)
	}

	// threshold is changeable
	pinger.Timeout = time.Second * 2
	pinger.Count = 1
	err = pinger.Run() // Blocks until finished.

	if err != nil {
		klog.ErrorS(err, "pinger runs wrong!")
		panic(err)
	}

	// 完成后删除这一部分
	stats := pinger.Statistics() // get send/receive/duplicate/rtt stats
	// klog.InfoS("rtt msg:", "targetIP:", stats.IPAddr.IP, "Avgrtt:", stats.AvgRtt, "PacketLoss:", stats.PacketLoss)
	return stats.MinRtt
}

// 删除节点的情况
// 算出来的概率太小为0的情况
// 优化代码结构
// 时延的结果可能可以优化
// node的变更情况，要不要每次都把map删掉
