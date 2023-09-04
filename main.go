package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// MultiWriter 一个同时向多个写入器写入的 io.Writer 实现
type MultiWriter struct {
	writers []io.Writer
}

func (mw *MultiWriter) Write(p []byte) (n int, err error) {
	for _, w := range mw.writers {
		n, err = w.Write(p)
		if err != nil || n != len(p) {
			return n, err
		}
	}
	return len(p), nil
}

func main() {

	//-----------------------创建logs.yaml文件-------------------------
	outputFileName := "logs.yaml"
	outputFile, err := os.Create(outputFileName)
	if err != nil {
		fmt.Println("无法创建输出文件:", err)
		os.Exit(1)
	}
	defer outputFile.Close()

	// 创建一个 MultiWriter，可以同时向控制台和文件写入。
	multiWriter := &MultiWriter{
		writers: []io.Writer{os.Stdout, outputFile},
	}

	//---------------------执行bke cluster create -f bkecluster.yaml命令---------------------
	createCmd := exec.Command("bke", "cluster", "create", "-f", "bkecluster.yaml")
	createCmd.Stdout = multiWriter
	createCmd.Stderr = multiWriter

	createErr := createCmd.Run()
	if createErr != nil {
		fmt.Println("无法执行 bke cluster create 命令:", createErr)
		os.Exit(1)
	}

	// -----------------------创建test.yaml文件-------------------------
	testFileName := "test.yaml"
	testFile, err := os.Create(testFileName)
	if err != nil {
		fmt.Println("无法创建测试输出文件:", err)
		os.Exit(1)
	}
	defer testFile.Close()

	// 暂停一分钟
	fmt.Println("等待1分钟...")
	time.Sleep(1 * time.Minute)

	// 捕获 bke cluster create 命令的输出
	createOutputBytes, err := ioutil.ReadFile(outputFileName)
	if err != nil {
		fmt.Println("无法读取输出文件:", err)
		os.Exit(1)
	}

	// 将第一个命令的输出写入到 test.yaml 文件中
	err = ioutil.WriteFile(outputFileName, createOutputBytes, 0644)
	if err != nil {
		fmt.Println("无法写入文件:", err)
		os.Exit(1)
	}

	// 执行命令: sshpass -p Beyond@123 scp root@10.50.8.80:/etc/kubernetes/admin.conf .
	scpCmd := exec.Command("sshpass", "-p", "Beyond@123", "scp", "root@10.50.8.80:/etc/kubernetes/admin.conf", ".")
	scpErr := scpCmd.Run()
	if scpErr != nil {
		fmt.Println("无法执行 scp 命令:", scpErr)
		os.Exit(1)
	}

	// 获取当前工作目录
	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Println("无法获取当前工作目录:", err)
		os.Exit(1)
	}

	// 读取 admin.conf 文件内容
	adminConfPath := filepath.Join(currentDir, "admin.conf")
	adminConfBytes, err := ioutil.ReadFile(adminConfPath)
	if err != nil {
		fmt.Println("无法读取 admin.conf 文件:", err)
		os.Exit(1)
	}

	// 将 server 地址从 https://127.0.0.1:6443 修改为 https://10.50.8.80:6443
	newAdminConfContent := strings.Replace(string(adminConfBytes), "server: https://127.0.0.1:6443", "server: https://10.50.8.80:6443", 1)

	// 将修改后的内容写回 admin.conf 文件
	err = ioutil.WriteFile(adminConfPath, []byte(newAdminConfContent), 0644)
	if err != nil {
		fmt.Println("无法写入 admin.conf 文件:", err)
		os.Exit(1)
	}

	// 创建 clientcmd 配置对象，使用当前目录下的 admin.conf
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(currentDir, "admin.conf")}, // 使用 filepath.Join 来连接路径
		&clientcmd.ConfigOverrides{CurrentContext: "bke-cluster-kubernetes-admin@bke-cluster"},
	).ClientConfig()
	if err != nil {
		fmt.Println("无法创建 clientcmd 配置:", err)
		os.Exit(1)
	}

	// 创建一个kubernetes客户端对象，用于调用API
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Println("无法创建kubernetes客户端:", err)
		os.Exit(1)
	}

	// 获取节点信息并将其信息写入文件
	if err := getAndWriteNodes(clientset, multiWriter); err != nil {
		fmt.Println("无法获取或写入 Node 信息:", err)
		os.Exit(1)
	}

	// -----------------集群部署测试-------------------

	// 检查节点的状态并将信息写入到 test.yaml 文件中
	if err := checkAndWriteNodesStatus(outputFileName, testFile); err != nil {
		fmt.Println("无法检查或写入节点状态:", err)
		os.Exit(1)
	}

	// 获取 Pod 的信息并将其信息写入文件
	if err := getAndWritePods(clientset, multiWriter); err != nil {
		fmt.Println("无法获取或写入 Pod 信息:", err)
		os.Exit(1)
	}

	// -----------------组件安装测试-------------------
	// 将组件安装检查结果写入到 test.yaml 文件中
	if err := writeComponentInstallationCheck(outputFileName, testFile); err != nil {
		fmt.Println("无法执行组件安装检查:", err)
		os.Exit(1)
	}

	// 集群缩容测试
	performClusterScaleDownTest(outputFileName, testFileName, testFile, clientset, multiWriter)

	//集群扩容测试
	performClusterScaleUpTest(outputFileName, testFileName, testFile, clientset, multiWriter)

	//集群删除测试
	deleteCluster(testFile)

	// 删除当前目录下的 cluster.yaml 文件
	if err := os.Remove("cluster.yaml"); err != nil {
		fmt.Println("无法删除 cluster.yaml 文件:", err)
		os.Exit(1)
	}

	// 删除当前目录下的 logs.yaml 文件
	if err := os.Remove("logs.yaml"); err != nil {
		fmt.Println("无法删除 logs.yaml 文件:", err)
		os.Exit(1)
	}

	// 删除当前目录下的 admin.conf 文件
	if err := os.Remove("admin.conf"); err != nil {
		fmt.Println("无法删除 admin.conf 文件:", err)
		os.Exit(1)
	}

	fmt.Println("结果已写入到", outputFileName)
	fmt.Println("测试结果已写入到", testFileName)
}

// formatAge接收一个时间戳，并返回自那时以来经过的时间的字符串表示，格式为hhm（小时分钟）
func formatAge(timestamp time.Time) string {
	duration := time.Since(timestamp)
	hours := int(duration.Hours())                   // 获取小时数
	minutes := int(math.Mod(duration.Minutes(), 60)) // 获取分钟数，取模60
	return fmt.Sprintf("%dh%dm", hours, minutes)     // 格式化字符串，包括小时和分钟
}

//获取pods信息
func getAndWritePods(clientset *kubernetes.Clientset, writer io.Writer) error {
	var output string
	output += "NAMESPACE     NAME                                       READY   STATUS              RESTARTS   AGE\n"

	namespaces, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, ns := range namespaces.Items {
		pods, err := clientset.CoreV1().Pods(ns.Name).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			continue
		}

		for _, pod := range pods.Items {
			var ready int
			var total int
			var podStatus string

			// 计算处于 "Running" 状态的容器数量以及总容器数量
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Ready {
					ready++
				}
				total++
			}

			// 检查是否有容器处于 "Running" 状态
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.State.Running != nil {
					podStatus = "Running"
					break
				}
			}

			// 检查 Pod 的阶段是否为 "Pending"、"Succeeded" 或 "Failed"
			if podStatus == "" {
				podStatus = string(pod.Status.Phase)
			}

			// 检查是否有容器处于 "CrashLoopBackOff" 状态
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.State.Waiting != nil && containerStatus.State.Waiting.Reason == "CrashLoopBackOff" {
					podStatus = "CrashLoopBackOff"
					break
				}
			}

			// 获取所有容器的最大重启次数
			var maxRestartCount int
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if int(containerStatus.RestartCount) > maxRestartCount {
					maxRestartCount = int(containerStatus.RestartCount)
				}
			}

			// 检查 Pod 是否已准备就绪
			var isReady bool
			for _, condition := range pod.Status.Conditions {
				if condition.Type == "ContainersReady" && condition.Status == "True" {
					isReady = true
					break
				}
			}

			// 如果 Pod 不处于就绪状态但状态为运行中，则将状态更改为不可用
			if !isReady && podStatus == "Running" {
				podStatus = "Unavailable"
			}
			// 将 Pod 信息追加到输出字符串中
			output += fmt.Sprintf("%-13s%-43s%d/%d   %-19s%-11d%-8s\n", ns.Name, pod.Name, ready, total, podStatus, maxRestartCount, formatAge(pod.CreationTimestamp.Time))
		}
	}

	// 将输出写入提供的写入器（writer）
	if _, err := io.WriteString(writer, output); err != nil {
		return err
	}

	return nil
}

func getAndWriteNodes(clientset *kubernetes.Clientset, writer io.Writer) error {
	var output string
	output += "\nNAME       STATUS   ROLES         AGE   VERSION\n"

	nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, node := range nodes.Items {
		// 获取节点的状态，如果节点有 Ready 状态条件，并且状态为 True，则认为节点是 Ready 的，否则认为节点是 NotReady 的
		var nodeStatus string
		for _, condition := range node.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				nodeStatus = "Ready"
				break
			}
		}
		if nodeStatus == "" {
			nodeStatus = "NotReady"
		}

		// 获取节点的角色，如果节点有 node-role.kubernetes.io/<role> 格式的标签，则认为节点具有该角色，否则认为节点没有角色
		var nodeRoles []string
		for label := range node.Labels {
			if strings.HasPrefix(label, "node-role.kubernetes.io/") {
				nodeRoles = append(nodeRoles, strings.TrimPrefix(label, "node-role.kubernetes.io/"))
			}
		}
		if len(nodeRoles) == 0 {
			nodeRoles = append(nodeRoles, "<none>")
		}
		// 将节点信息追加到输出字符串中
		output += fmt.Sprintf("%-10s%-9s%-14s%-6s%-7s\n", node.Name, nodeStatus, strings.Join(nodeRoles, ","), formatAge(node.CreationTimestamp.Time), node.Status.NodeInfo.KubeletVersion)
	}

	// 将输出写入提供的写入器（writer）
	if _, err := io.WriteString(writer, output); err != nil {
		return err
	}

	return nil
}

func checkAndWriteNodesStatus(outputFileName string, testFile *os.File) error {
	_, err := testFile.WriteString("集群部署测试:\n")
	if err != nil {
		fmt.Println("无法写入测试输出文件:", err)
		os.Exit(1)
	}
	// 读取输出文件（logs.yaml）的内容
	outputContent, err := ioutil.ReadFile(outputFileName)
	if err != nil {
		return err
	}

	// 将输出内容拆分为行
	lines := strings.Split(string(outputContent), "\n")

	// 标志以检查是否有节点处于未就绪状态
	anyNodeNotReady := false

	// 遍历每一行并处理节点的状态
	for _, line := range lines {
		if strings.Contains(line, "[bke-node]") {
			parts := strings.SplitN(line, "]", 3)
			if len(parts) == 3 && strings.Contains(parts[2], "Node is not ready") {
				// 节点未准备就绪
				anyNodeNotReady = true
				break
			}
		}
	}

	// 根据节点的状态将信息写入到 test.yaml 文件中
	if anyNodeNotReady {
		// 节点未准备就绪
		_, err := testFile.WriteString("\nNAME       STATUS   ROLES         AGE   VERSION\n")
		if err != nil {
			return err
		}

		// 再次遍历每一行并写入节点的状态
		for _, line := range lines {
			if strings.Contains(line, "[bke-node]") {
				_, err := testFile.WriteString(line + "\n")
				if err != nil {
					return err
				}
			}
		}
	} else {
		// 所有节点都已准备就绪
		_, err := testFile.WriteString("success!\n\n")
		if err != nil {
			return err
		}
	}

	return nil
}

func writeComponentInstallationCheck(outputFileName string, testFile *os.File) error {
	// 将标题写入 test.yaml 文件
	_, err := testFile.WriteString("组件安装测试:\n")
	if err != nil {
		return err
	}

	// 读取输出文件（logs.yaml）的内容
	logsContent, err := ioutil.ReadFile(outputFileName)
	if err != nil {
		return err
	}

	lines := strings.Split(string(logsContent), "\n")
	writeOutput := false

	// 遍历 logs.yaml 文件中的每一行
	for _, line := range lines {
		if writeOutput {
			// 检查每行是否包含 "Running" 或 "Succeeded"
			if !strings.Contains(line, "Running") && !strings.Contains(line, "Succeeded") {
				// 将包含 "Running" 或 "Succeeded" 的 STATUS 行写入 test.yaml 文件
				_, err := testFile.WriteString(line + "\n")
				if err != nil {
					return err
				}
			}
		} else if strings.Contains(line, "NAMESPACE     NAME                                       READY   STATUS") {
			// 从下一行开始写入输出
			writeOutput = true
			// 将标题写入 test.yaml 文件
			_, err := testFile.WriteString(line + "\n")
			if err != nil {
				return err
			}
		}
	}

	// 如果没有发现非 "Running" 或 "Succeeded" 的问题，写入 "success!" 到 test.yaml 文件
	if !writeOutput {
		_, err := testFile.WriteString("success!\n")
		if err != nil {
			return err
		}
	}

	return nil
}

//-----------------集群缩容测试--------------------
func performClusterScaleDownTest(outputFileName, testFileName string, testFile *os.File, clientset *kubernetes.Clientset, multiWriter *MultiWriter) {

	fmt.Println("..........集群缩容测试..........")

	// 写入测试标题
	if testFile != nil {
		_, err := testFile.WriteString("集群缩容测试:\n")
		if err != nil {
			fmt.Println("无法写入测试输出文件:", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("无法写入测试输出文件: 文件为空")
		os.Exit(1)
	}

	modifyBKEClusterConfigDown()

	// 暂停30秒
	fmt.Println("等待30秒...")
	time.Sleep(30 * time.Second)

	for elapsed := 0; elapsed < 300; elapsed += 30 {
		// 清空 logs.yaml 文件
		if err := ioutil.WriteFile(outputFileName, []byte(""), 0644); err != nil {
			fmt.Println("无法清空输出文件:", err)
			os.Exit(1)
		}

		// 执行 getAndWriteNodes 来获取节点状态并将信息写入 logs.yaml
		if err := getAndWriteNodes(clientset, multiWriter); err != nil {
			fmt.Println("无法获取节点信息或写入节点状态:", err)
			os.Exit(1)
		}

		// 读取输出文件（logs.yaml）的内容
		logsContent, err := ioutil.ReadFile(outputFileName)
		if err != nil {
			fmt.Println("无法读取输出文件:", err)
			os.Exit(1)
		}

		// 检查日志内容中是否存在 "worker-3" 节点
		if !strings.Contains(string(logsContent), "worker-3") {
			// "worker-3" 节点不存在，将 "success!" 写入 test.yaml 并退出循环
			fmt.Println("success!")
			if testFile != nil {
				testFile.WriteString("success!\n\n")
			}
			break
		}

		// 在下一次迭代之前等待30秒
		fmt.Println("等待30秒...")
		time.Sleep(30 * time.Second)

		// 清空 logs.yaml 文件
		if err := ioutil.WriteFile(outputFileName, []byte(""), 0644); err != nil {
			fmt.Println("无法清空输出文件:", err)
			os.Exit(1)
		}
	}

	// 如果经过了5分钟（300秒），则检查输出文件（logs.yaml）是否为空
	logsContent, err := ioutil.ReadFile(outputFileName)
	if err != nil {
		fmt.Println("无法读取输出文件:", err)
		os.Exit(1)
	}
	if len(logsContent) == 0 { // 如果输出文件为空，则写入 "集群缩容失败" 到 test.yaml
		if testFile != nil {
			testFile.WriteString("failed\n\n")
		}
	}

}

func modifyBKEClusterConfigDown() {
	// 执行命令：kubectl get bkecluster bke-cluster -n bke-cluster -o yaml > cluster.yaml
	getCmd := exec.Command("kubectl", "get", "bkecluster", "bke-cluster", "-n", "bke-cluster", "-o", "yaml")
	getCmd.Stdout, _ = os.Create("cluster.yaml") // 将标准输出重定向到文件

	// 运行获取命令
	if err := getCmd.Run(); err != nil { // 使用 "Run" 方法执行命令并等待其完成
		fmt.Println("无法执行 kubectl get 命令:", err)
		os.Exit(1)
	}

	// 打开 YAML 文件
	file, err := os.OpenFile("cluster.yaml", os.O_RDWR, 0644) // 使用 OpenFile 函数来获取一个 *os.File 对象
	if err != nil {
		fmt.Println("无法打开配置文件:", err)
		os.Exit(1)
	}
	defer file.Close()

	// 读取 YAML 内容
	data, err := ioutil.ReadAll(file)
	if err != nil {
		fmt.Println("无法读取配置文件:", err)
		os.Exit(1)
	}
	yamlContent := string(data)

	// 修改 YAML 内容
	yamlContent = strings.Replace(yamlContent, "bke.bocloud.com/appointment-deleted-nodes: \"\"", "bke.bocloud.com/appointment-deleted-nodes: \"10.50.8.49\"", 1)
	yamlContent = strings.Replace(yamlContent, "- hostname: worker-3\n      ip: 10.50.8.49\n      password: L2IKDzrf0XEYVFGx0dV2/A==\n      port: \"22\"\n      role:\n      - node\n      username: root", "", 1)

	// 将修改后的 YAML 重新写入配置文件
	file.Truncate(0)                                         // 将修改后的 YAML 重新写回配置文件
	file.Seek(0, io.SeekStart)                               // 将文件指针移动到文件的开头
	if _, err := file.WriteString(yamlContent); err != nil { // 使用 WriteString 方法来写入修改后的 YAML 内容
		fmt.Println("无法写入配置文件:", err)
		os.Exit(1)
	}

	// 执行命令：kubectl apply -f cluster.yaml
	applyCmd := exec.Command("kubectl", "apply", "-f", "cluster.yaml")

	// 捕获标准输出和错误流
	var stdout, stderr bytes.Buffer
	applyCmd.Stdout = &stdout
	applyCmd.Stderr = &stderr

	// 运行 apply 命令
	if err := applyCmd.Run(); err != nil { // 使用 "Run" 方法执行该命令并等待其完成
		fmt.Println("无法执行 kubectl apply 命令:", err)
		fmt.Println("错误输出:", stderr.String())
		os.Exit(1)
	}

	// 获取 apply 命令的输出
	output := stdout.String()
	fmt.Println(output) // 将输出打印到控制台
}

//---------集群扩容测试-------------
func performClusterScaleUpTest(outputFileName, testFileName string, testFile *os.File, clientset *kubernetes.Clientset, multiWriter *MultiWriter) {
	fmt.Println("..........集群扩容测试..........")

	// 写入测试标题
	if testFile != nil {
		_, err := testFile.WriteString("集群扩容测试:\n")
		if err != nil {
			fmt.Println("无法写入测试输出文件:", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("无法写入测试输出文件: 文件为空")
		os.Exit(1)
	}

	modifyBKEClusterConfigUp()

	// 暂停30秒
	fmt.Println("等待30秒...")
	time.Sleep(30 * time.Second)

	for elapsed := 0; elapsed < 300; elapsed += 30 {
		// 清空 logs.yaml 文件
		if err := ioutil.WriteFile(outputFileName, []byte(""), 0644); err != nil {
			fmt.Println("无法清空输出文件:", err)
			os.Exit(1)
		}

		// 执行 getAndWriteNodes 来获取节点状态并将信息写入 logs.yaml
		if err := getAndWriteNodes(clientset, multiWriter); err != nil {
			fmt.Println("无法获取节点信息或写入节点状态:", err)
			os.Exit(1)
		}

		// 读取输出文件（logs.yaml）的内容
		logsContent, err := ioutil.ReadFile(outputFileName)
		if err != nil {
			fmt.Println("无法读取输出文件:", err)
			os.Exit(1)
		}

		// 检查日志内容中是否存在 "worker-3" 节点
		if strings.Contains(string(logsContent), "worker-3") {
			// "worker-3" 节点不存在，将 "success!" 写入 test.yaml 并退出循环
			fmt.Println("success!")
			if testFile != nil {
				testFile.WriteString("success!\n\n")
			}
			break
		}

		// 在下一次迭代之前等待30秒
		fmt.Println("等待30秒...")
		time.Sleep(30 * time.Second)

		// 清空 logs.yaml 文件
		if err := ioutil.WriteFile(outputFileName, []byte(""), 0644); err != nil {
			fmt.Println("无法清空输出文件:", err)
			os.Exit(1)
		}
	}

	// 如果经过了5分钟（300秒），则检查输出文件（logs.yaml）是否为空
	logsContent, err := ioutil.ReadFile(outputFileName)
	if err != nil {
		fmt.Println("无法读取输出文件:", err)
		os.Exit(1)
	}
	if len(logsContent) == 0 { // 如果输出文件为空，则写入 "集群扩容失败" 到 test.yaml
		if testFile != nil {
			testFile.WriteString("failed!\n\n")
		}
	}
}

func modifyBKEClusterConfigUp() {
	// 执行命令：kubectl get bkecluster bke-cluster -n bke-cluster -o yaml > cluster.yaml
	getCmd := exec.Command("kubectl", "get", "bkecluster", "bke-cluster", "-n", "bke-cluster", "-o", "yaml")
	getCmd.Stdout, _ = os.Create("cluster.yaml") // 重定向标准输出到文件

	// 运行获取命令
	if err := getCmd.Run(); err != nil {
		fmt.Println("无法执行 kubectl get 命令:", err)
		os.Exit(1)
	}

	// 打开 YAML 文件
	file, err := os.OpenFile("cluster.yaml", os.O_RDWR, 0644)
	if err != nil {
		fmt.Println("无法打开配置文件:", err)
		os.Exit(1)
	}
	defer file.Close()

	// 读取 YAML 内容
	data, err := ioutil.ReadAll(file)
	if err != nil {
		fmt.Println("无法读取配置文件:", err)
		os.Exit(1)
	}
	yamlContent := string(data)

	// 在 spec:nodes: 字段下添加一段内容
	addition := `- hostname: worker-3
      ip: 10.50.8.49
      password: L2IKDzrf0XEYVFGx0dV2/A==
      port: "22"
      role:
      - node
      username: root`

	// 找到 spec:nodes: 字段，将内容添加到其中
	yamlContent = strings.Replace(yamlContent, "spec:nodes:", "spec:nodes:\n"+addition, 1)

	// 将修改后的 YAML 重新写入配置文件
	if err := ioutil.WriteFile("cluster.yaml", []byte(yamlContent), 0644); err != nil {
		fmt.Println("无法写入配置文件:", err)
		os.Exit(1)
	}

	// 执行命令: kubectl apply -f cluster.yaml
	applyCmd := exec.Command("kubectl", "apply", "-f", "cluster.yaml")

	// 捕获标准输出和错误流
	var stdout, stderr bytes.Buffer
	applyCmd.Stdout = &stdout
	applyCmd.Stderr = &stderr

	// 运行应用命令
	if err := applyCmd.Run(); err != nil {
		fmt.Println("无法执行 kubectl apply 命令:", err)
		fmt.Println("错误输出:", stderr.String())
		os.Exit(1)
	}

	// 获取应用命令的输出
	output := stdout.String()
	fmt.Println(output)
}

func deleteCluster(testFile *os.File) {
	fmt.Println("..........集群删除测试..........")

	// 写入测试标题
	if testFile != nil {
		_, err := testFile.WriteString("集群删除测试:\n")
		if err != nil {
			fmt.Println("无法写入测试输出文件:", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("无法写入测试输出文件: 文件为空")
		os.Exit(1)
	}

	// 执行命令：kubectl get bkecluster bke-cluster -n bke-cluster -o yaml > cluster.yaml
	getCmd := exec.Command("kubectl", "get", "bkecluster", "bke-cluster", "-n", "bke-cluster", "-o", "yaml")
	getCmd.Stdout, _ = os.Create("cluster.yaml") // 重定向标准输出到文件

	// 运行获取命令
	if err := getCmd.Run(); err != nil {
		fmt.Println("无法执行 kubectl get 命令:", err)
		os.Exit(1)
	}

	// 打开 YAML 文件
	file, err := os.OpenFile("cluster.yaml", os.O_RDWR, 0644)
	if err != nil {
		fmt.Println("无法打开配置文件:", err)
		os.Exit(1)
	}
	defer file.Close()

	// 读取 YAML 内容
	data, err := ioutil.ReadAll(file)
	if err != nil {
		fmt.Println("无法读取配置文件:", err)
		os.Exit(1)
	}
	yamlContent := string(data)

	// 修改 YAML 内容
	yamlContent = strings.Replace(yamlContent, "bke.bocloud.com/ignore-namespace-delete: \"true\"", "bke.bocloud.com/ignore-namespace-delete: \"false\"", -1)
	yamlContent = strings.Replace(yamlContent, "bke.bocloud.com/ignore-target-cluster-delete: \"true\"", "bke.bocloud.com/ignore-target-cluster-delete: \"false\"", -1)

	// 将修改后的 YAML 重新写入配置文件
	if err := ioutil.WriteFile("cluster.yaml", []byte(yamlContent), 0644); err != nil {
		fmt.Println("无法写入配置文件:", err)
		os.Exit(1)
	}

	// 执行命令: kubectl apply -f cluster.yaml
	applyCmd := exec.Command("kubectl", "apply", "-f", "cluster.yaml")

	// 捕获标准输出和错误流
	var stdout, stderr bytes.Buffer
	applyCmd.Stdout = &stdout
	applyCmd.Stderr = &stderr

	// 运行应用命令
	if err := applyCmd.Run(); err != nil {
		fmt.Println("无法执行 kubectl apply 命令:", err)
		fmt.Println("错误输出:", stderr.String())
		os.Exit(1)
	}

	// 执行命令：kubectl delete -f bkecluster.yaml
	deleteCmd := exec.Command("kubectl", "delete", "-f", "bkecluster.yaml")

	// 捕获标准输出和错误流
	deleteStdout, deleteStderr := bytes.Buffer{}, bytes.Buffer{}
	deleteCmd.Stdout = &deleteStdout
	deleteCmd.Stderr = &deleteStderr

	// 运行删除命令
	if err := deleteCmd.Run(); err != nil {
		fmt.Println("无法执行 kubectl delete 命令:", err)
		fmt.Println("错误输出:", deleteStderr.String())
		os.Exit(1)
	}

	// 获取删除命令的输出
	deleteOutput := deleteStdout.String()
	fmt.Println(deleteOutput)

	if testFile != nil {
		_, err := testFile.WriteString("success\n")
		if err != nil {
			fmt.Println("无法写入测试输出文件:", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("无法写入测试输出文件: 文件为空")
		os.Exit(1)
	}
}
