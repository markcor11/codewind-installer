/*******************************************************************************
 * Copyright (c) 2019 IBM Corporation and others.
 * All rights reserved. This program and the accompanying materials
 * are made available under the terms of the Eclipse Public License v2.0
 * which accompanies this distribution, and is available at
 * http://www.eclipse.org/legal/epl-v20.html
 *
 * Contributors:
 *     IBM Corporation - initial API and implementation
 *******************************************************************************/

package remote

import (
	"os"
	"path/filepath"

	logr "github.com/sirupsen/logrus"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// RemoveDeploymentOptions : Deployment removal options
type RemoveDeploymentOptions struct {
	Namespace   string
	WorkspaceID string
}

const (
	// ResourceNotProcessed : Resource not processed
	ResourceNotProcessed = 0
	// ResourceFound : Resource located
	ResourceFound = 1
	// ResourceNotFound : Resource could not be located
	ResourceNotFound = 2
	// ResourceRemoved : Resource was successfully removed
	ResourceRemoved = 3
	// ResourceSkipped : Resource was not removed and was skipped
	ResourceSkipped = 4
	// ResourceRemoveFailed : Resource removal failed
	ResourceRemoveFailed = 5
)

// RemovalResult : Status for each component
type RemovalResult struct {

	// Pods
	StatusPODGatekeeper  int
	StatusPODPFE         int
	StatusPODPerformance int
	StatusPODKeycloak    int

	// Services
	StatusServiceGatekeeper  int
	StatusServicePFE         int
	StatusServicePerformance int
	StatusServiceKeycloak    int

	// Deployments
	StatusDeploymentGatekeeper  int
	StatusDeploymentPFE         int
	StatusDeploymentPerformance int
	StatusDeploymentKeycloak    int

	// Secrets
	StatusSecretsCodewindClient  int
	StatusSecretsCodewindSession int
	StatusSecretsCodewindTLS     int
	StatusSecretsKeycloakTLS     int
	StatusSecretsKeycloakUser    int

	// Service account
	StatusServiceAccount int

	// Cluster role bindings
	ClusterRoleBindings int

	// Persistent volume claims
	StatusPVCKeycloak int
}

// RemoveRemote : Remove remote install from Kube
func RemoveRemote(remoteRemovalOptions *RemoveDeploymentOptions) (*RemovalResult, *RemInstError) {

	removalStatus := RemovalResult{
		StatusPODGatekeeper:  ResourceNotProcessed,
		StatusPODPFE:         ResourceNotProcessed,
		StatusPODPerformance: ResourceNotProcessed,
		StatusPODKeycloak:    ResourceNotProcessed,

		StatusServiceGatekeeper:  ResourceNotProcessed,
		StatusServicePFE:         ResourceNotProcessed,
		StatusServicePerformance: ResourceNotProcessed,
		StatusServiceKeycloak:    ResourceNotProcessed,

		StatusDeploymentGatekeeper:  ResourceNotProcessed,
		StatusDeploymentPFE:         ResourceNotProcessed,
		StatusDeploymentPerformance: ResourceNotProcessed,
		StatusDeploymentKeycloak:    ResourceNotProcessed,

		StatusSecretsCodewindClient:  ResourceNotProcessed,
		StatusSecretsCodewindSession: ResourceNotProcessed,
		StatusSecretsCodewindTLS:     ResourceNotProcessed,
		StatusSecretsKeycloakTLS:     ResourceNotProcessed,
		StatusSecretsKeycloakUser:    ResourceNotProcessed,

		StatusServiceAccount: ResourceNotProcessed,
		ClusterRoleBindings:  ResourceNotProcessed,
		StatusPVCKeycloak:    ResourceNotProcessed,
	}

	namespace := remoteRemovalOptions.Namespace

	kubeConfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		logr.Infof("Unable to retrieve Kubernetes Config %v\n", err)
		return nil, &RemInstError{errOpNotFound, err, err.Error()}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logr.Infof("Unable to retrieve Kubernetes clientset %v\n", err)
		return nil, &RemInstError{errOpNotFound, err, err.Error()}
	}

	// Check if namespace exists
	logr.Infof("Checking namespace %v exists\n", namespace)
	_, err = clientset.CoreV1().Namespaces().Get(namespace, v1.GetOptions{})
	if err != nil {
		logr.Error("Unable to locate %v namespace: %v", namespace, err)
		return nil, &RemInstError{errOpCreateNamespace, err, err.Error()}
	}
	logr.Infof("Found '%v' namespace\n", namespace)

	// // Locate Codewind PFE POD
	// podList, err := clientset.CoreV1().Pods(namespace).List(
	// 	v1.ListOptions{LabelSelector: "app=codewind-pfe,codewindWorkspace=" + remoteRemovalOptions.WorkspaceID},
	// )

	// if err != nil {
	// 	logr.Errorf("Unable to find the Codewind PFE POD '%v'", remoteRemovalOptions.WorkspaceID)
	// 	removalStatus.StatusPODPFE = ResourceNotFound
	// }

	// if podList != nil && podList.Items != nil && len(podList.Items) == 1 {
	// 	deletePod(remoteRemovalOptions, clientset, "app=codewind-pfe,codewindWorkspace="+remoteRemovalOptions.WorkspaceID, "Codewind PFE")
	// }

	status, err := deleteDeployment(remoteRemovalOptions, clientset, "app="+PFEPrefix+",codewindWorkspace="+remoteRemovalOptions.WorkspaceID, "Codewind PFE")
	removalStatus.StatusDeploymentPFE = status
	status, err = deleteDeployment(remoteRemovalOptions, clientset, "app="+PerformancePrefix+",codewindWorkspace="+remoteRemovalOptions.WorkspaceID, "Codewind Performance")
	removalStatus.StatusDeploymentPerformance = status
	status, err = deleteDeployment(remoteRemovalOptions, clientset, "app="+GatekeeperPrefix+",codewindWorkspace="+remoteRemovalOptions.WorkspaceID, "Codewind Gatekeeper")
	removalStatus.StatusDeploymentGatekeeper = status

	status, err = deleteService(remoteRemovalOptions, clientset, "app="+PFEPrefix+",codewindWorkspace="+remoteRemovalOptions.WorkspaceID, "Codewind PFE")
	removalStatus.StatusServicePFE = status
	status, err = deleteService(remoteRemovalOptions, clientset, "app="+PerformancePrefix+",codewindWorkspace="+remoteRemovalOptions.WorkspaceID, "Codewind Performance")
	removalStatus.StatusServicePerformance = status
	status, err = deleteService(remoteRemovalOptions, clientset, "app="+GatekeeperPrefix+",codewindWorkspace="+remoteRemovalOptions.WorkspaceID, "Codewind Gatekeeper")
	removalStatus.StatusServiceGatekeeper = status

	return &removalStatus, nil
}

func deleteDeployment(remoteRemovalOptions *RemoveDeploymentOptions, clientset *kubernetes.Clientset, labelSelector string, title string) (int, error) {
	phase := ResourceNotProcessed
	deploymentList, err := clientset.AppsV1().Deployments(remoteRemovalOptions.Namespace).List(
		v1.ListOptions{LabelSelector: labelSelector},
	)
	logr.Infof("Searching for '%v' deployment", title)
	if err != nil {
		logr.Warnf("Unable to find the '%v' deployment", title)
		return ResourceNotFound, err
	}
	if deploymentList != nil && deploymentList.Items != nil && len(deploymentList.Items) == 1 {
		logr.Infof("Found deployment '%v'", title)
		phase = ResourceFound
		deploymentName := deploymentList.Items[0].GetName()
		err := clientset.AppsV1().Deployments(remoteRemovalOptions.Namespace).Delete(deploymentName, nil)
		if err != nil {
			logr.Errorf("Failed to remove deployment '%v'", deploymentName)
			phase = ResourceRemoveFailed
			return phase, err
		}
		logr.Infof("Removed Deployment '%v'", deploymentName)
		phase = ResourceRemoved
	}
	return phase, nil
}

func deletePod(remoteRemovalOptions *RemoveDeploymentOptions, clientset *kubernetes.Clientset, labelSelector string, title string) (int, error) {
	phase := ResourceNotProcessed
	podList, err := clientset.CoreV1().Pods(remoteRemovalOptions.Namespace).List(
		v1.ListOptions{LabelSelector: labelSelector},
	)
	logr.Infof("Searching for '%v' pod", title)
	if err != nil {
		logr.Warnf("Unable to find the '%v' pod '%v'", title, remoteRemovalOptions.WorkspaceID)
		return ResourceNotFound, err
	}
	if podList != nil && podList.Items != nil && len(podList.Items) == 1 {
		logr.Infof("Found POD %v", title)
		phase = ResourceFound
		podName := podList.Items[0].GetName()
		err := clientset.CoreV1().Pods(remoteRemovalOptions.Namespace).Delete(podName, nil)
		if err != nil {
			logr.Errorf("Failed to remove pod '%v'", podName)
			phase = ResourceRemoveFailed
			return phase, err
		}
		logr.Infof("Removed POD '%v'", podName)
		phase = ResourceRemoved
	}
	return phase, nil
}

func deleteService(remoteRemovalOptions *RemoveDeploymentOptions, clientset *kubernetes.Clientset, labelSelector string, title string) (int, error) {
	phase := ResourceNotProcessed
	serviceList, err := clientset.CoreV1().Services(remoteRemovalOptions.Namespace).List(
		v1.ListOptions{LabelSelector: labelSelector},
	)
	logr.Infof("Searching for '%v' service", title)
	if err != nil {
		logr.Warnf("Unable to find the '%v' service '%v'", title, remoteRemovalOptions.WorkspaceID)
		return ResourceNotFound, err
	}
	if serviceList != nil && serviceList.Items != nil && len(serviceList.Items) == 1 {
		logr.Infof("Found Service '%v'", title)
		phase = ResourceFound
		serviceName := serviceList.Items[0].GetName()
		err := clientset.CoreV1().Services(remoteRemovalOptions.Namespace).Delete(serviceName, nil)
		if err != nil {
			logr.Errorf("Failed to remove service '%v'", serviceName)
			phase = ResourceRemoveFailed
			return phase, err
		}
		logr.Infof("Removed Service '%v'", serviceName)
		phase = ResourceRemoved
	}
	return phase, nil
}
