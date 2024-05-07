package internal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/prometheus/client_golang/api"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"github.com/prometheus/client_golang/api/prometheus/v1"
)

type HTTPResponse struct {
	Body        string      `json:"body,omitempty"`
}

const (
	RootDir = "/home/ec2-user"
	CLUSTER = "benchmark-cluster"
	REGION  = "ca-central-1"
	JmxFile = "benchmark-test.jmx"
)

type RequestHandler struct {

}

func NewRequestHandler() *RequestHandler{
	handler :=RequestHandler{}
	return &handler
}

func (handler *RequestHandler) CreateCluster(response http.ResponseWriter, request *http.Request) {

	headers := request.Header
	project := headers.Get("project")
	projectHost := headers.Get("ProjectHost")
	if project == "" {
		errorMessage := "Missing or empty 'project' eader"
		http.Error(response, errorMessage, http.StatusBadRequest)
		return
	}
	profileName := project+"-profile"

	sourceDir := RootDir + "/namespaces/default"
	destDir := RootDir + "/namespaces/" + project
	err := copyDir(sourceDir, destDir)
	if err != nil {
		_ = removeProjDirectory(project)
		errorMessage := fmt.Sprintf("Error copying files to new directory %v: %v", project, err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}
	_,err = createJmeterCluster(project, profileName)
	if err != nil {
		deleteCluster(project, profileName)
		errorMessage := fmt.Sprintf("Error creating jmeter cluster: %v", err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}

	if projectHost != ""{
		err = editPrometheusConfigMap(project, projectHost)
		if err != nil {
			deleteCluster(project, profileName)
			errorMessage := fmt.Sprintf("Error updating prometheus config: %v", err)
			http.Error(response, errorMessage, http.StatusInternalServerError)
			return
		}
		_, err = deployPrometheusConfigMap(profileName, project)
		if err != nil {
			deleteCluster(project, profileName)
			errorMessage := fmt.Sprintf("Error creating prometheus configmap: %v", err)
			http.Error(response, errorMessage, http.StatusInternalServerError)
			return
		}
		_, err = deployPrometheus(profileName, project)
		if err != nil {
			deleteCluster(project, profileName)
			errorMessage := fmt.Sprintf("Error creating prometheus: %v", err)
			http.Error(response, errorMessage, http.StatusInternalServerError)
			return
		}
	}
	err = addNamespaceToFile(project)

	res := new(HTTPResponse)
	res.Body = "Success"
	jsonResponse, _ := json.Marshal(res)
	response.Header().Set("Content-Type", "application/json")
	contentLength := len(jsonResponse)
	response.Header().Set("Content-Length", strconv.Itoa(contentLength))
	response.WriteHeader(http.StatusOK)
	response.Write(jsonResponse)
}

func (handler *RequestHandler) StartTest(response http.ResponseWriter, request *http.Request) {
	headers := request.Header
	project := headers.Get("project")
	instances := headers.Get("instances")
	threads := headers.Get("threads")
	if project == "" {
		errorMessage := "Missing or empty 'project' eader"
		http.Error(response, errorMessage, http.StatusBadRequest)
		return
	}
	profileName := project+"-profile"
	if threads == ""{
		threads = "1000"
	}
	if instances == ""{
		instances = "1"
	}
	scale, err := strconv.Atoi(instances)
	if err != nil {
		// Handle conversion error
		http.Error(response, "Error converting instances to integer:", http.StatusInternalServerError)
		return
	}
	err = request.ParseMultipartForm(10 << 20)
	if err != nil {
		http.Error(response, "Unable to parse form", http.StatusInternalServerError)
		return
	}

	file, _, err := request.FormFile("file")
	if err != nil {
		http.Error(response, "Unable to retrieve file from form", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	err = copyJmxFile(project, file)
	if err != nil {
		http.Error(response, "Unable to copy jmx file", http.StatusInternalServerError)
		return
	}
	if scale > 1 {
		_, err = scaleCluster(profileName, instances)
		time.Sleep(100000)
		if err != nil {
			http.Error(response, "Unable to scale instances", http.StatusInternalServerError)
			return
		}
	}
	result, err := runJmeterCluster(project, threads)
	res := new(HTTPResponse)
	res.Body = string(result)
	jsonResponse, _ := json.Marshal(res)
	response.Header().Set("Content-Type", "application/json")
	contentLength := len(jsonResponse)
	response.Header().Set("Content-Length", strconv.Itoa(contentLength))
	response.WriteHeader(http.StatusOK)
	response.Write(jsonResponse)
}

func (handler *RequestHandler) DownloadResults(response http.ResponseWriter, request *http.Request) {
	headers := request.Header
	project := headers.Get("project")
	if project == ""  {
		errorMessage := "Missing or empty 'project' header"
		http.Error(response, errorMessage, http.StatusBadRequest)
		return
	}
	_, err := downloadResult(project)
	if err != nil {
		errorMessage := fmt.Sprintf("Unable to download test result: %v", err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}

	reportFileName, err := zipAndCopyDirectory(project)
	if err != nil {
		errorMessage := fmt.Sprintf("Unable zip report file %v", err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}
	file, err := os.Open(reportFileName)
	if err != nil {
		errorMessage := fmt.Sprintf("File not found: %v", err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}
	defer file.Close()
	_, err = file.Stat()
	if err != nil {
		http.Error(response, "Error getting file information", http.StatusInternalServerError)
		return
	}
	// Copy the file to the response writer
	_, err = io.Copy(response, file)
	if err != nil {
		http.Error(response, "Error serving file", http.StatusInternalServerError)
		return
	}
	_, err = cleanupReport(project)
	if err != nil {
		errorMessage := fmt.Sprintf("Unable clear report file %v", err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}
	// Set the appropriate headers for file download
	response.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", "report.zip"))
	response.Header().Set("Content-Type", "application/zip")
	//response.Header().Set("Content-Length", fmt.Sprint(fileInfo.Size()))
}

func (handler *RequestHandler) ClearCluster(response http.ResponseWriter, request *http.Request) {

	headers := request.Header
	project := headers.Get("project")
	if project == "" {
		errorMessage := "Missing or empty 'project' eader"
		http.Error(response, errorMessage, http.StatusBadRequest)
		return
	}
	profileName := project+"-profile"
	_,err := clearJmeterCluster(project, profileName)
	if err != nil {
		errorMessage := fmt.Sprintf("Error deleting jmeter cluster: %v", err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}
	err = removeProjDirectory(project)
	if err != nil {
		return
	}
	err = removeNamespaceFromFile(project)
	res := new(HTTPResponse)
	res.Body = "Success"
	jsonResponse, _ := json.Marshal(res)
	response.Header().Set("Content-Type", "application/json")
	contentLength := len(jsonResponse)
	response.Header().Set("Content-Length", strconv.Itoa(contentLength))
	response.WriteHeader(http.StatusOK)
	response.Write(jsonResponse)
}

func (handler *RequestHandler) GetSystemPerformanceReport(response http.ResponseWriter, request *http.Request) {
	headers := request.Header
	project := headers.Get("project")
	if project == "" {
		errorMessage := "Missing or empty 'project' header"
		http.Error(response, errorMessage, http.StatusBadRequest)
		return
	}
	err := getMemoryUsageFromPrometheus(project)
	if err != nil {
		errorMessage := fmt.Sprintf("Error reading memory from prometheus: %v", err)
		http.Error(response, errorMessage, http.StatusInternalServerError)
		return
	}
	res := new(HTTPResponse)
	res.Body = "Success"
	jsonResponse, _ := json.Marshal(res)
	response.Header().Set("Content-Type", "application/json")
	contentLength := len(jsonResponse)
	response.Header().Set("Content-Length", strconv.Itoa(contentLength))
	response.WriteHeader(http.StatusOK)
	response.Write(jsonResponse)
}

func (handler *RequestHandler) CleanupScheduler() {
	filePath := RootDir +"/namespaces/namespace_names.txt"

	go func() {
		for range time.Tick(30 * time.Minute) {

			namespaceNames, err := readNamespaceNames(filePath)
			if err != nil {
				continue
			}
			deleteOldNamespaces(namespaceNames)
		}
	} ()
}

func deleteCluster(project string, profileName string) {

	_,err := clearJmeterCluster(project, profileName)
	if err != nil {
		return
	}
	err = removeProjDirectory(project)
	if err != nil {
		return
	}
	err = removeNamespaceFromFile(project)
}

func removeProjDirectory(projectDir string) error{
	projectDir = RootDir +"/namespaces/"+projectDir
	err := os.RemoveAll(projectDir)
	if err != nil {
		return err
	}
	return nil
}

func copyJmxFile(project string, file multipart.File) error{
	destPath := filepath.Join(RootDir+"/namespaces/"+project, JmxFile)
	destFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()
	_, err = io.Copy(destFile, file)
	if err != nil {
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	return nil
}

func copyDir(src, dst string) error {
	files, err := ioutil.ReadDir(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, os.ModePerm); err != nil {
		return err
	}

	for _, file := range files {
		srcPath := filepath.Join(src, file.Name())
		dstPath := filepath.Join(dst, file.Name())

		if file.IsDir() {
			// Recursively copy subdirectory
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Copy file and set execute permissions
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}

			if err := os.Chmod(dstPath, 0755); err != nil {
				return err
			}
		}
	}

	return nil
}

func createJmeterCluster(project string, namespace string) ([]byte, error){
	command := RootDir +"/namespaces/"+project+"/command_jmeter_cluster_create.sh"
	args := []string{namespace, RootDir +"/namespaces/"+project}
	return execCommand(command, args)
}

func clearJmeterCluster(project string, namespace string) ([]byte, error){
	command := RootDir +"/namespaces/"+project+"/command_jmeter_cluster_clear.sh"
	args := []string{namespace, RootDir +"/namespaces/"+project}
	return execCommand(command, args)
}


func runJmeterCluster(project string, threads string) ([]byte, error){
	command := RootDir +"/namespaces/"+project+"/command_start_test.sh"
	args := []string{"-f"+ RootDir +"/namespaces/"+project+"/"+ JmxFile, "-Gthreads="+threads, RootDir +"/namespaces/"+project}
	return execCommand(command, args)
}

func deployPrometheusConfigMap(profile string, project string) ([]byte, error){
	filePath := RootDir +"/namespaces/"+project+"/prometheus_configmap.yaml"
	command := "kubectl"
	args := []string{"-n", profile, "apply", "-f", filePath}
	return execCommand(command, args)
}

func deployPrometheus(profile string, project string) ([]byte, error){
	filePath := RootDir +"/namespaces/"+project+"/prometheus_deploy.yaml"
	command := "kubectl"
	args := []string{"-n", profile, "apply", "-f", filePath}
	return execCommand(command, args)
}

func scaleCluster(profile string, instancesToScale  string) ([]byte, error){
	command := "kubectl"
	args := []string{"-n", profile, "scale", "deployment/jmeter-slaves", "--replicas", instancesToScale}
	return execCommand(command, args)
}

func downloadResult(project string) ([]byte, error){
	command := "bash"
	args := []string{"-c", "cd " + RootDir + "/namespaces/" + project + " && ./command_download_test_result.sh"}
	return execCommand(command, args)
}

func zipAndCopyDirectory(project string)(string, error){
	command := "bash"
	args := []string{"-c", "cd " + RootDir + "/namespaces/" + project + " && zip -r report.zip report"}
	_,err := execCommand(command, args)
	if err != nil {
		return "", err
	}
	return RootDir +"/namespaces/"+project+"/report.zip", nil
}

func cleanupReport(project string)(string, error){
	command := "bash"
	args := []string{"-c", "cd " + RootDir + "/namespaces/" + project + " && rm -rf report.zip report"}
	_,err := execCommand(command, args)
	if err != nil {
		return "", err
	}
	return "success",nil
}

func execCommand(command string, args []string) ([]byte, error){
	// Create a new command
	cmd := exec.Command(command, args...)

	// Capture the command's output
	result, err := cmd.CombinedOutput()
	if err != nil {
		// Handle the error
		return nil,err
	}

	return result, nil
}


// readNamespaceNames reads namespace names from a file
func readNamespaceNames(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var namespaceNames []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		namespaceNames = append(namespaceNames, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return namespaceNames, nil
}

// deleteOldNamespaces deletes namespaces created 6 hours before the current time
func deleteOldNamespaces(namespaceNames []string) {
	for _, project := range namespaceNames {
		namespace := project+"-profile"
		creationTime, err := getNamespaceCreationTime(namespace)
		if err != nil {
			continue
		}
		sixHoursAgo := time.Now().Add(-6 * time.Hour)
		if creationTime.Before(sixHoursAgo) {
			deleteCluster(project, namespace)
		}
	}
}

// getNamespaceCreationTime retrieves the creation time of a namespace using kubectl
func getNamespaceCreationTime(namespace string) (time.Time, error) {

	cmd := exec.Command("kubectl", "get", "namespace", namespace, "-ojsonpath='{.metadata.creationTimestamp}'")
	output, err := cmd.Output()
	if err != nil {
		return time.Time{}, err
	}

	creationTime, err := time.Parse(time.RFC3339, strings.Trim(string(output), "'"))
	if err != nil {
		return time.Time{}, err
	}

	return creationTime, nil
}

func removeNamespaceFromFile(namespaceName string) error {
	filePath := RootDir + "/namespaces/namespace_names.txt"
	existingNamespaces, err := readNamespaceNames(filePath)
	if err != nil {
		return err
	}

	// Check if the namespace exists in the file
	found := false
	for i, name := range existingNamespaces {
		if name == namespaceName {
			// Remove the namespace from the slice
			existingNamespaces = append(existingNamespaces[:i], existingNamespaces[i+1:]...)
			found = true
			break
		}
	}

	// If the namespace was not found, return an error
	if !found {
		return fmt.Errorf("Namespace '%s' not found in file '%s'", namespaceName, filePath)
	}

	// Write the updated namespace names back to the file
	err = writeNamespaceNames(existingNamespaces, filePath)
	if err != nil {
		return err
	}

	return nil
}


func writeNamespaceNames(namespaces []string, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	for _, namespace := range namespaces {
		_, err := file.WriteString(namespace + "\n")
		if err != nil {
			return err
		}
	}

	return nil
}


func addNamespaceToFile(namespaceName string) error {

	filePath := RootDir + "/namespaces/namespace_names.txt"
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Append the namespace name to the file
	_, err = file.WriteString(namespaceName + "\n")
	if err != nil {
		return err
	}

	return nil
}

func editPrometheusConfigMap(project string, host string) error{

	projectDir := RootDir +"/namespaces/"+project
	content, err := ioutil.ReadFile(projectDir+"/prometheus_configmap.yaml")
	if err != nil {
		errorMessage := fmt.Sprintf("Error opening configmap: %v", err)
		return fmt.Errorf(errorMessage)
	}

	newContent := strings.Replace(string(content), "${PROMETHEUS_TARGET}", host, 1)

	err = ioutil.WriteFile(projectDir+"/prometheus_configmap.yaml", []byte(newContent), 0644)
	if err != nil {
		errorMessage := fmt.Sprintf("Error writing configmap: %v", err)
		return fmt.Errorf(errorMessage)
	}
	return nil
}

func getMemoryUsageFromPrometheus(project string) error{
	//namespace, _ := getTenantName(project)
	//podName := "192.168.108.83"

	/*command := "kubectl"
	args := []string{"get", "po", podName, "-n", namespace, "-o wide | awk '{print $6}'"}
	output, err := execCommand(command, args)
	if err != nil {
		return err
	}*/
	podIP := "192.168.108.83"

	// Prometheus API URL
	promURL := fmt.Sprintf("http://%s:9090", podIP)

	// Create a Prometheus API client
	client, err := api.NewClient(api.Config{
		Address: promURL,
	})
	if err != nil {
		return err
	}

	// Create a Prometheus API v1 client
	promAPI := v1.NewAPI(client)

	// Query parameters
	query := "node_memory_Active_bytes"
	duration := 5 * time.Minute
	end := time.Now()
	start := end.Add(-duration)

	// Execute the query
	result, warnings, err := promAPI.QueryRange(context.TODO(), query, v1.Range{
		Start: start,
		End:   end,
		Step:  time.Minute,
	})
	if err != nil {
		return err
	}

	if len(warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range warnings {
			fmt.Printf("%s\n", warning)
		}
	}

	// Print the result
	fmt.Printf("Query Result:\n%s\n", result)

	return nil
}

func getTenantName(project string) (string, error){
	workingdir := RootDir +"/namespaces/"+project
	command := "awk"
	args := []string{"{print $NF}", workingdir+"/tenant_export"}
	output, err := execCommand(command, args)
	if err != nil || len(output) == 0 {
		return "", err
	}
	result := strings.TrimSpace(string(output))
	return result, nil
}


func getPodName(project string) (string, error){
	command := "bash"
	args := []string{"-c", RootDir+"/benchmark-manager/internal/scripts/get_pod_name.sh", RootDir+"/namespaces/"+project}
	output, err := execCommand(command, args)
	fmt.Sprintf("got pod = %v", output)
	if err != nil {
		fmt.Printf("Error executing command: %v\n", err)
		return "", err
	}

	// Convert the byte slice to a string
	result := strings.TrimSpace(string(output))

	return result, nil
}


