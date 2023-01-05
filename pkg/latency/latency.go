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

	//
	stats := pinger.Statistics() // get send/receive/duplicate/rtt stats
	return stats.MinRtt
}
