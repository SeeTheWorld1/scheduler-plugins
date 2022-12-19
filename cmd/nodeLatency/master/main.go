package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/gin-gonic/gin"
)

// save the latency from this node to other nodes
var nodeLatency = map[string]map[string]float64{} // 两倍空间的存储方式

type nodeBaseInformation struct {
	nodeName string
	nodeIp   string
}

type controller struct {
	nodeList map[string]bool
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
		nodeList: map[string]bool{},
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
	if _, ok := c.nodeList[node.Name]; ok {
		return
	}
	c.nodeList[node.Name] = true
}

func (c *controller) delete(obj interface{}) {
	node := obj.(*corev1.Node)
	delete(c.nodeList, node.Name)

	// Clear related data
	if _, ok := nodeLatency[node.Name]; ok {
		delete(c.nodeList, node.Name)
	}
	for key, nodeMap := range nodeLatency {
		if _, ok := nodeMap[node.Name]; ok {
			delete(nodeMap, node.Name) //不知道这里是否是修改的原map
			nodeLatency[key] = nodeMap
		}
	}
}

func (c *controller) runWorker() {
	c.getNodeLatency()
}

// 定义接收数据的结构体
type Login struct {
	// binding:"required"修饰的字段，若接收为空值，则报错，是必须字段
	Hostname string             `form:"username" json:"hostname" uri:"user" xml:"user" binding:"required"`
	Latenecy map[string]float64 `form:"password" json:"latenecy" uri:"password" xml:"password" binding:"required"`
}

func (c *controller) getNodeLatency() {
	// 1.创建路由
	// 默认使用了2个中间件Logger(), Recovery()
	r := gin.Default()
	// JSON绑定
	r.POST("go", func(c *gin.Context) {
		// 声明接收的变量
		var json Login
		// 将request的body中的数据，自动按照json格式解析到结构体
		if err := c.ShouldBindJSON(&json); err != nil {
			// 返回错误信息
			// gin.H封装了生成json数据的工具
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// 判断用户名密码是否正确
		setNodeLatency(json.Hostname, json.Latenecy)
		fmt.Println(nodeLatency)
		if json.Hostname != "DESKTOP-JGAPSRA" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "304"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "200"})
	})
	r.Run(":8000")
}

func setNodeLatency(hostname string, latencyMap map[string]float64) {
	nodeMap, ok := nodeLatency[hostname]
	if !ok {
		nodeMap = map[string]float64{}
	}
	for nodeName, latency := range latencyMap {
		if _, ok := nodeMap[nodeName]; !ok {
			nodeMap[nodeName] = latency
		}
	}
	nodeLatency[hostname] = nodeMap
}
