package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"path/filepath"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/gin-gonic/gin"
)

type nodeLatency struct {
	nodelatency map[string]map[string]float64
	mu          sync.RWMutex
}

// save the latency from this node to other nodes
var interNodesLatency = nodeLatency{
	nodelatency: map[string]map[string]float64{}, // have repeated data(e.g. [a][b], [b][a])
}

type nodeBaseInformation struct {
	nodeName string
	nodeIp   string
}

type nodesSet map[string]bool

type controller struct {
	nodeList nodesSet
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
		nodeList: nodesSet{},
	}

	// 使用自定义 handler
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctl.add,
		UpdateFunc: func(interface{}, interface{}) {}, // do nothing
		DeleteFunc: ctl.delete,
	})

	go ctl.runWorker()
	go initRpcServer()
	<-stopper
}

func (c *controller) add(obj interface{}) {
	node := obj.(*corev1.Node)

	if _, ok := c.nodeList[node.Name]; ok {
		return
	}
	c.nodeList[node.Name] = true
	// Set the latency from the node to its own network as a default value : 0.02
	interNodesLatency.mu.Lock()
	if _, ok := interNodesLatency.nodelatency[node.Name]; !ok {
		interNodesLatency.nodelatency[node.Name] = make(map[string]float64)
	}
	interNodesLatency.nodelatency[node.Name][node.Name] = 0.02
	interNodesLatency.mu.Unlock()
}

// Clear related data about the deleted node.
func (c *controller) delete(obj interface{}) {
	node := obj.(*corev1.Node)
	delete(c.nodeList, node.Name)

	// Clear related data
	interNodesLatency.mu.Lock()
	if _, ok := interNodesLatency.nodelatency[node.Name]; ok {
		delete(interNodesLatency.nodelatency, node.Name) //这里应该是nodeLatency
	}
	for key, nodeMap := range interNodesLatency.nodelatency {
		if _, ok := nodeMap[node.Name]; ok {
			delete(nodeMap, node.Name)
			interNodesLatency.nodelatency[key] = nodeMap
		}
	}
	interNodesLatency.mu.Unlock()
}

func (c *controller) runWorker() {
	c.getNodeLatency()
}

// 定义接收数据的结构体
type DistanceBetweenNodes struct {
	// binding:"required"修饰的字段，若接收为空值，则报错，是必须字段
	Hostname string             `form:"hostname" json:"hostname" uri:"hostname" xml:"hostname" binding:"required"`
	Latenecy map[string]float64 `form:"latenecy" json:"latenecy" uri:"latenecy" xml:"latenecy" binding:"required"`
}

// the http server
// it is used to collect the network distance between nodes.
func (c *controller) getNodeLatency() {
	// 创建路由
	// 默认使用了2个中间件Logger(), Recovery()
	r := gin.Default()
	// JSON绑定
	r.POST("go", func(c *gin.Context) {
		// 声明接收的变量
		var dbn DistanceBetweenNodes
		// 将request的body中的数据，自动按照json格式解析到结构体
		if err := c.ShouldBindJSON(&dbn); err != nil {
			// 返回错误信息
			// gin.H封装了生成json数据的工具
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		setNodeLatency(dbn.Hostname, dbn.Latenecy)
		// handle error
		// ...

		c.JSON(http.StatusOK, gin.H{"status": "200"})
	})
	r.Run(":8000")
}

// 将节点发来的到新的到其他节点的网络距离信息存储到内存中
func setNodeLatency(hostname string, latencyMap map[string]float64) {
	interNodesLatency.mu.Lock()
	nodeMap, ok := interNodesLatency.nodelatency[hostname]
	if !ok {
		nodeMap = make(map[string]float64)
	}
	for nodeName, latency := range latencyMap {
		if _, ok := nodeMap[nodeName]; ok {
			continue
		}
		//
		_, ok := interNodesLatency.nodelatency[nodeName]
		if !ok {
			interNodesLatency.nodelatency[nodeName] = make(map[string]float64)
		}
		x, ok := interNodesLatency.nodelatency[nodeName][hostname]
		if ok {
			latency = minFloat64(x, latency)
		}
		interNodesLatency.nodelatency[nodeName][hostname] = latency
		nodeMap[nodeName] = latency
	}
	interNodesLatency.nodelatency[hostname] = nodeMap
	interNodesLatency.mu.Unlock()
}

func minFloat64(x, y float64) float64 {
	if x < y {
		return x
	}
	return y
}

// Args is related to the preScoreState implemented in the nodeLatency plug-in
// CandidateNodes is a set of candidate nodes
type Args struct {
	CandidateNodes       []string
	Dependencies         [][]string
	NodesDependOnThisPod []string
}

// ServiceA Customize a struct type.
// it is used to register an rpc service
type ServiceA struct{}

// init rpc server
func initRpcServer() {
	service := new(ServiceA)
	rpc.Register(service) // register RPC service.
	l, e := net.Listen("tcp", ":9091")
	if e != nil {
		log.Fatal("listen error:", e)
	}
	for {
		conn, _ := l.Accept()
		rpc.ServeConn(conn)
	}
}

// Score is used to store the final scores of each candidate nodes.
type Score struct {
	Scores map[string]int64
}

// Add an exportable method GetNodesScores for ServiceA type
func (s *ServiceA) GetNodesScores(args *Args, res *Score) error {
	// init nodes score map
	res.Scores = make(map[string]int64)

	lC, lD, lN := len(args.CandidateNodes), len(args.Dependencies), len(args.NodesDependOnThisPod)
	// If no nodes are available for selection
	// return
	if lC == 0 {
		return nil
	}
	if lD == 0 && lN == 0 {
		return nil
	}
	//
	if lC == 1 {
		res.Scores[args.CandidateNodes[0]] = 100
		return nil
	}

	scoresSlice := make([]int64, lC)
	avgLatency := make([]float64, lC)
	// calculate the scores about NodesDependOnThisPod
	if lN > 0 {
		for i, candidateNode := range args.CandidateNodes {
			var sum float64 = 0
			for _, nodeName := range args.NodesDependOnThisPod {
				sum += interNodesLatency.nodelatency[candidateNode][nodeName]
			}
			avgLatency[i] = sum / float64(lN)
		}
		// normalize score
		minL, maxL := 10000.0, 0.0
		for _, L := range avgLatency {
			if L > maxL {
				maxL = L
			}
			if L < minL {
				minL = L
			}
		}
		if maxL == minL {
			for i := 0; i < lC; i++ {
				scoresSlice[i] += 100
			}
		} else {
			divisionL := maxL - minL
			for i := 0; i < lC; i++ {
				scoresSlice[i] += (100 - int64((avgLatency[i]-minL)/divisionL*100))
			}
		}
	}
	fmt.Println(scoresSlice)

	//  calculate the scores about NodesDependOnThisPod
	if lD > 0 {
		for j := 0; j < lD; j++ {
			for i, candidateNode := range args.CandidateNodes {
				var sum float64 = 0
				for _, nodeName := range args.Dependencies[j] {
					nL := interNodesLatency.nodelatency[candidateNode][nodeName]
					if nL == 0 {
						return fmt.Errorf("%s has 0 latency with %s", candidateNode, nodeName)
					}
					sum += 1 / nL
				}
				avgLatency[i] = sum / float64(len(args.Dependencies[j]))
			}
			// normalize score
			minL, maxL := 10000.0, 0.0
			for _, L := range avgLatency {
				if L > maxL {
					maxL = L
				}
				if L < minL {
					minL = L
				}
			}
			if maxL == minL {
				for i := 0; i < lC; i++ {
					scoresSlice[i] += 100
				}
			} else {
				divisionL := maxL - minL
				for i := 0; i < lC; i++ {
					scoresSlice[i] += int64((avgLatency[i] - minL) / divisionL * 100)
				}
			}
		}
	}

	// calculate the final score of each node
	if lN > 0 {
		lD += 1
	}
	fmt.Println(scoresSlice)
	for i, s := range scoresSlice {
		res.Scores[args.CandidateNodes[i]] = s / int64(lD)
	}

	return nil
}
