package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/scheduler-plugins/pkg/latency"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// save the latency from this node to other nodes
type nodesDistance struct {
	mu            sync.Mutex
	nodesdistance map[string]float64
}

var nodeLatency = &nodesDistance{
	nodesdistance: make(map[string]float64),
}

// master ip
var masterIp string = ""

type nodeBaseInformation struct {
	nodeName string
	nodeIp   string
}

type controller struct {
	nodeLister listerv1.NodeLister
	workQueue  []nodeBaseInformation
	mu         sync.Mutex
}

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err)
	}

	// 初始化 client
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Panic(err.Error())
	}

	stopper := make(chan struct{})
	defer close(stopper)

	// 初始化 informer
	factory := informers.NewSharedInformerFactory(clientset, 0)
	nodeInformer := factory.Core().V1().Nodes()
	informer := nodeInformer.Informer()
	defer runtime.HandleCrash()

	// 启动 informer，list & watch
	go factory.Start(stopper)

	// 从 apiserver 同步资源，即 list
	if !cache.WaitForCacheSync(stopper, informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	ctl := &controller{
		nodeLister: nodeInformer.Lister(),
		workQueue:  []nodeBaseInformation{},
	}

	// 使用自定义 handler
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctl.add,
		UpdateFunc: func(interface{}, interface{}) {}, // do nothing
		DeleteFunc: ctl.delete,
	})

	go ctl.runWorker()
	<-stopper
}

// add will add the new node to the workQueue.
func (c *controller) add(obj interface{}) {
	node := obj.(*corev1.Node)

	nodeLatency.mu.Lock()
	if _, ok := nodeLatency.nodesdistance[node.Name]; ok {
		return
	}
	nodeLatency.mu.Unlock()

	// if this node is master, record its ip
	if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
		masterIp = node.Status.Addresses[0].Address
	}
	if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
		masterIp = node.Status.Addresses[0].Address
	}

	newNode := nodeBaseInformation{
		nodeName: node.Name,
		nodeIp:   node.Status.Addresses[0].Address,
	}
	c.mu.Lock()
	c.workQueue = append(c.workQueue, newNode)
	c.mu.Unlock()

}

// delete will delete the related data about the deleted node.
func (c *controller) delete(obj interface{}) {
	node := obj.(*corev1.Node)
	nodeLatency.mu.Lock()
	delete(nodeLatency.nodesdistance, node.Name)
	nodeLatency.mu.Unlock()
}

// run the processsItem function in a loop.
func (c *controller) runWorker() {
	for {
		c.processItem()
	}
}

func (c *controller) processItem() {
	newNodeNums := 0
	newNodeLatency := map[string]float64{}
	for len(c.workQueue) > 0 {
		c.mu.Lock()
		newNodeNums++
		newNode := c.workQueue[0]
		c.workQueue = c.workQueue[1:]

		// calculate the latency to this node
		t := latency.GetNodeLatency(newNode.nodeIp)
		nodeLatency.mu.Lock()
		nodeLatency.nodesdistance[newNode.nodeName] = t.Seconds() * 1e3 // ms
		nodeLatency.mu.Unlock()
		newNodeLatency[newNode.nodeName] = t.Seconds() * 1e3
		c.mu.Unlock()
	}

	// Notify the master of new information
	if newNodeNums > 0 {
		fmt.Println(newNodeLatency)
		notifyMasterNewInformation(newNodeLatency)
	}

}

type result struct {
	Status string `json:"status"`
}

// Notify the master of the network distance information to the new nodes.
func notifyMasterNewInformation(newNodeLatency map[string]float64) {
	hostName, err := os.Hostname()
	if err != nil {
		panic(err)
	}

	data := map[string]interface{}{}
	data["hostname"] = hostName
	data["latenecy"] = newNodeLatency

	bytesData, _ := json.Marshal(data)

	masterUrl := "http://" + masterIp + ":8000/go"
	resp, err := http.Post(masterUrl, "application/json", bytes.NewReader(bytesData))
	if err != nil {
		fmt.Printf("error is %v", err)
		os.Exit(0)
	}

	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)

	var res result
	json.Unmarshal(body, &res)

	fmt.Println(res.Status)
}
