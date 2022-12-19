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
var nodeLatency = map[string]float64{}

type nodeBaseInformation struct {
	nodeName string
	nodeIp   string
}

type controller struct {
	nodeLister listerv1.NodeLister
	workQueue  []nodeBaseInformation
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

	// nodeList, err := c.nodeLister.List(labels.Everything())
	// if err != nil {
	// 	fmt.Println(err)
	// }
	// nodeNameList := []string{}
	// for _, node := range nodeList {
	// 	nodeNameList = append(nodeNameList, node.Status.Addresses[0].Address)
	// }
	// fmt.Println("nodelist:", nodeNameList)

	go ctl.runWorker()
	<-stopper
}

func (c *controller) add(obj interface{}) {
	node := obj.(*corev1.Node)
	//fmt.Println("add a node:", node.Name)
	if _, ok := nodeLatency[node.Name]; ok {
		return
	}

	newNode := nodeBaseInformation{
		nodeName: node.Name,
		nodeIp:   node.Status.Addresses[0].Address,
	}
	c.workQueue = append(c.workQueue, newNode)

}

func (c *controller) delete(obj interface{}) {
	node := obj.(*corev1.Node)
	delete(nodeLatency, node.Name)
}

func (c *controller) runWorker() {
	for {
		c.processItem()
	}
}

func (c *controller) processItem() {
	newNodeNums := 0
	newNodeLatency := map[string]float64{}
	for len(c.workQueue) > 0 {
		newNodeNums++
		newNode := c.workQueue[0]
		// here the queue operation may need a lock
		c.workQueue = c.workQueue[1:]

		// calculate the latency to this node
		t := latency.GetNodeLatency(newNode.nodeIp)
		nodeLatency[newNode.nodeName] = t.Seconds() * 1e3 // ms
		newNodeLatency[newNode.nodeName] = t.Seconds() * 1e3
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

func notifyMasterNewInformation(newNodeLatency map[string]float64) {
	hostName, err := os.Hostname()
	if err != nil {
		panic(err)
	}

	data := map[string]interface{}{}
	data["hostname"] = hostName
	data["latenecy"] = newNodeLatency

	bytesData, _ := json.Marshal(data)

	resp, err := http.Post("http://127.0.0.1:8000/go", "application/json", bytes.NewReader(bytesData))
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
