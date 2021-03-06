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

package project

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eclipse/codewind-installer/pkg/config"
	"github.com/eclipse/codewind-installer/pkg/connections"
	"github.com/eclipse/codewind-installer/pkg/sechttp"
	"github.com/eclipse/codewind-installer/pkg/utils"
	"github.com/urfave/cli"
)

type (
	// CompleteRequest is the request body format for calling the upload complete API
	CompleteRequest struct {
		FileList      []string `json:"fileList"`
		DirectoryList []string `json:"directoryList"`
		ModifiedList  []string `json:"modifiedList"`
		TimeStamp     int64    `json:"timeStamp"`
	}

	// FileUploadMsg is the message sent on uploading a file
	FileUploadMsg struct {
		IsDirectory  bool   `json:"isDirectory"`
		Mode         uint   `json:"mode"`
		RelativePath string `json:"path"`
		Message      string `json:"msg"`
	}

	// UploadedFile is the file to sync
	UploadedFile struct {
		FilePath   string `json:"filePath"`
		Status     string `json:"status"`
		StatusCode int    `json:"statusCode"`
	}

	// SyncResponse is the status of the file syncing
	SyncResponse struct {
		Status        string         `json:"status"`
		StatusCode    int            `json:"statusCode"`
		UploadedFiles []UploadedFile `json:"uploadedFiles"`
	}

	// walkerInfo is the input struct to the walker function
	walkerInfo struct {
		Path         string   // the path of the current file
		os.FileInfo           // the FileInfo of the current file
		IgnoredPaths []string // paths to ignore
		LastSync     int64    // last sync time
	}

	// SyncInfo contains the information from a project sync
	SyncInfo struct {
		fileList         []string
		directoryList    []string
		modifiedList     []string
		UploadedFileList []UploadedFile
	}

	// refPath is a referenced file path to sync
	refPath struct {
		From string `json:"from"`
		To   string `json:"to"`
	}

	// refPaths is an array of refPath objects
	refPaths struct {
		RefPaths []refPath
	}
)

// SyncProject syncs a project with its remote connection
func SyncProject(c *cli.Context) (*SyncResponse, *ProjectError) {
	var currentSyncTime = time.Now().UnixNano() / 1000000
	projectPath := strings.TrimSpace(c.String("path"))
	projectID := strings.TrimSpace(c.String("id"))
	synctime := int64(c.Int("time"))

	conID, projErr := GetConnectionID(projectID)

	if projErr != nil {
		return nil, projErr
	}

	connection, conInfoErr := connections.GetConnectionByID(conID)
	if conInfoErr != nil {
		return nil, &ProjectError{errOpConNotFound, conInfoErr, conInfoErr.Desc}
	}

	conURL, conURLErr := config.PFEOriginFromConnection(connection)
	if conURLErr != nil {
		return nil, &ProjectError{errOpConNotFound, conURLErr.Err, conURLErr.Desc}
	}

	// if local path doesn't exist but is equal to the locOnDisk, the directory has likely been deleted
	// emit this message to the UI socket by calling the PFE /missingLocalDir API
	pathExists := utils.PathExists(projectPath)

	if !pathExists {
		projectInfo, err := GetProjectFromID(&http.Client{}, connection, conURL, projectID)
		if err != nil {
			return nil, err
		}
		newErr := fmt.Errorf(textProjectPathDoesNotExist)

		if projectPath != projectInfo.LocationOnDisk {
			return nil, &ProjectError{errBadPath, newErr, newErr.Error()}
		}

		err = handleMissingProjectDir(&http.Client{}, connection, conURL, projectID)
		if err != nil {
			return nil, &ProjectError{errBadPath, err, err.Error()}
		}

		return nil, &ProjectError{errBadPath, newErr, newErr.Error()}
	}

	// Sync all the necessary project files
	syncInfo, syncErr := syncFiles(&http.Client{}, projectPath, projectID, conURL, synctime, connection)

	// Add a check here for files that have been imported into the project, compare lists of files
	BeforeFileList, err := GetProjectFileList(&http.Client{}, connection, conURL, projectID)
	if err == nil {
		added := findNewFiles(&http.Client{}, projectID, BeforeFileList, syncInfo.fileList, projectPath, connection, conURL)
		// Add any new files to the modifiedList
		for _, file := range added {
			syncInfo.modifiedList = append(syncInfo.modifiedList, file)
		}
	}

	// Complete the upload
	completeRequest := CompleteRequest{
		FileList:      syncInfo.fileList,
		DirectoryList: syncInfo.directoryList,
		ModifiedList:  syncInfo.modifiedList,
		TimeStamp:     currentSyncTime,
	}
	completeStatus, completeStatusCode := completeUpload(&http.Client{}, projectID, completeRequest, connection, conURL)
	response := SyncResponse{
		UploadedFiles: syncInfo.UploadedFileList,
		Status:        completeStatus,
		StatusCode:    completeStatusCode,
	}

	return &response, syncErr
}

func syncFiles(client utils.HTTPClient, projectPath string, projectID string, conURL string, synctime int64, connection *connections.Connection) (*SyncInfo, *ProjectError) {
	var fileList []string
	var directoryList []string
	var modifiedList []string
	var uploadedFiles []UploadedFile

	refPathsChanged := false

	// define a walker function
	walker := func(path string, info walkerInfo, err error) error {
		if err != nil {
			return err
			// TODO - How to handle *some* files being unreadable
		}

		// If it is the top level directory ignore it
		if path == projectPath {
			return nil
		}

		// use ToSlash to try and get both Windows and *NIX paths to be *NIX for pfe
		relativePath := filepath.ToSlash(path[(len(projectPath) + 1):])

		if !info.IsDir() {
			shouldIgnore := ignoreFileOrDirectory(relativePath, false, info.IgnoredPaths)
			if shouldIgnore {
				return nil
			}
			// Create list of all files for a project
			fileList = append(fileList, relativePath)

			// get time file was modified in milliseconds since epoch
			modifiedmillis := info.ModTime().UnixNano() / 1000000
			// Has this file been modified since last sync
			if modifiedmillis > info.LastSync {
				uploadResponse := syncFile(&http.Client{}, projectID, projectPath, info.Path, connection, conURL)
				uploadedFiles = append(uploadedFiles, uploadResponse)
				// Create list of all modfied files
				modifiedList = append(modifiedList, relativePath)

				// if this file changed, it should force referenced files to re-sync
				if relativePath == ".cw-refpaths.json" {
					refPathsChanged = true
				}
			}
		} else {
			shouldIgnore := ignoreFileOrDirectory(relativePath, true, info.IgnoredPaths)
			if shouldIgnore {
				return filepath.SkipDir
			}
			directoryList = append(directoryList, relativePath)
		}
		return nil
	}

	// read the ignored and referenced paths into lists
	cwSettingsIgnoredPathsList := retrieveIgnoredPathsList(projectPath)
	cwRefPathsList := retrieveRefPathsList(projectPath)

	// initialize a combined list, prime it with ignored paths from .cw-settings
	// then append with referenced "To" paths
	cwCombinedIgnoredPathsList := append([]string{}, cwSettingsIgnoredPathsList...)
	for _, refPath := range cwRefPathsList {
		cwCombinedIgnoredPathsList = append(cwCombinedIgnoredPathsList, refPath.To)
	}

	// first sync files that are physically in the project
	err := filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
		// use combined ignored paths here, files in the project that
		// are also the target of a reference should not be synced
		wInfo := walkerInfo{
			path,
			info,
			cwCombinedIgnoredPathsList,
			synctime,
		}
		return walker(path, wInfo, err)
	})
	if err != nil {
		text := fmt.Sprintf("error walking the path %q: %v\n", projectPath, err)
		return nil, &ProjectError{errOpSync, errors.New(text), text}
	}

	errText := ""

	// then sync referenced file paths
	for _, refPath := range cwRefPathsList {

		// get From path and resolve to absolute if needed
		from := refPath.From
		if !filepath.IsAbs(from) {
			from = filepath.Join(projectPath, from)
		}

		// get info on the referenced file; skip invalid paths
		info, err := os.Stat(from)
		if err != nil || info.IsDir() {
			text := fmt.Sprintf("invalid file reference %q: %v\n", from, err)
			errText += text
			continue
		}

		lastSync := synctime
		// force re-sync if .cw-refpaths.json itself was changed
		if refPathsChanged {
			lastSync = 0
		}

		// now pass it to the walker function
		wInfo := walkerInfo{
			from,
			info,
			cwSettingsIgnoredPathsList,
			lastSync,
		}
		// "To" path is relative to the project
		walker(filepath.Join(projectPath, refPath.To), wInfo, nil)
	}

	if errText != "" {
		return &SyncInfo{fileList, directoryList, modifiedList, uploadedFiles}, &ProjectError{errOpSyncRef, errors.New(errText), errText}
	}

	return &SyncInfo{fileList, directoryList, modifiedList, uploadedFiles}, nil
}

func completeUpload(client utils.HTTPClient, projectID string, completeRequest CompleteRequest, conInfo *connections.Connection, conURL string) (string, int) {
	uploadEndURL := conURL + "/api/v1/projects/" + projectID + "/upload/end"
	jsonPayload, _ := json.Marshal(&completeRequest)
	req, err := http.NewRequest("POST", uploadEndURL, bytes.NewBuffer(jsonPayload))
	if err != nil {
		fmt.Printf("error creating request %v\n", err)
		return err.Error(), 0
	}

	req.Header.Set("Content-Type", "application/json")
	resp, httpSecError := sechttp.DispatchHTTPRequest(client, req, conInfo)
	if httpSecError != nil {
		fmt.Printf("error making request  %v\n", httpSecError)
		return httpSecError.Desc, 0
	}
	defer resp.Body.Close()

	return resp.Status, resp.StatusCode
}

// Retrieve the ignoredPaths list from a .cw-settings file
func retrieveIgnoredPathsList(projectPath string) []string {
	cwSettingsPath := filepath.Join(projectPath, ".cw-settings")
	var cwSettingsIgnoredPathsList []string
	if _, err := os.Stat(cwSettingsPath); !os.IsNotExist(err) {
		plan, _ := ioutil.ReadFile(cwSettingsPath)
		var cwSettingsJSON CWSettings
		err = json.Unmarshal(plan, &cwSettingsJSON)
		if err == nil {
			cwSettingsIgnoredPathsList = cwSettingsJSON.IgnoredPaths
		}
	}
	return cwSettingsIgnoredPathsList
}

// Retrieve the refPaths list from a .cw-refpaths.json file
func retrieveRefPathsList(projectPath string) []refPath {
	cwRefPathsPath := filepath.Join(projectPath, ".cw-refpaths.json")
	var cwRefPathsList []refPath
	if _, err := os.Stat(cwRefPathsPath); !os.IsNotExist(err) {
		plan, _ := ioutil.ReadFile(cwRefPathsPath)
		var cwRefPathsJSON refPaths
		err = json.Unmarshal(plan, &cwRefPathsJSON)
		if err == nil {
			cwRefPathsList = cwRefPathsJSON.RefPaths
		}
	}
	return cwRefPathsList
}

func ignoreFileOrDirectory(name string, isDir bool, cwSettingsIgnoredPathsList []string) bool {
	isFileInIgnoredList := false
	for _, fileName := range cwSettingsIgnoredPathsList {
		fileName = filepath.Clean(fileName)
		// remove preceding slash from older versions of cw-settings
		if strings.HasPrefix(fileName, "/") {
			fileName = string([]rune(fileName)[1:])
		}
		matched, err := filepath.Match(fileName, name)
		if err != nil {
			return false
		}
		if matched {
			isFileInIgnoredList = true
			break
		}
	}
	return isFileInIgnoredList
}

// handleMissingProjectDir : Respond to a local project dir not existing
func handleMissingProjectDir(httpClient utils.HTTPClient, connection *connections.Connection, url, projectID string) *ProjectError {
	req, requestErr := http.NewRequest("POST", url+"/api/v1/projects/"+projectID+"/missingLocalDir", nil)
	if requestErr != nil {
		return &ProjectError{errOpRequest, requestErr, requestErr.Error()}
	}

	resp, httpSecError := sechttp.DispatchHTTPRequest(httpClient, req, connection)
	if httpSecError != nil {
		return &ProjectError{errOpRequest, httpSecError, httpSecError.Desc}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respErr := fmt.Errorf("Error: PFE responded with status code %d", resp.StatusCode)
		return &ProjectError{errOpNotFound, respErr, respErr.Error()}
	}

	return nil
}

func findNewFiles(client utils.HTTPClient, projectID string, beforefiles []string, afterfiles []string, projectPath string, connection *connections.Connection, conURL string) []string {
	var newfiles []string
	for _, filename := range afterfiles {
		if !existsIn(filename, beforefiles) {
			fullPath := filepath.Join(projectPath, filename)
			syncFile(&http.Client{}, projectID, projectPath, fullPath, connection, conURL)
			newfiles = append(newfiles, filename)
		}
	}
	return newfiles
}

func existsIn(value string, slice []string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
}

func syncFile(client utils.HTTPClient, projectID string, projectPath string, path string, connection *connections.Connection, conURL string) UploadedFile {
	// use ToSlash to try and get both Windows and *NIX paths to be *NIX for pfe
	relativePath := filepath.ToSlash(path[(len(projectPath) + 1):])
	uploadResponse := UploadedFile{
		FilePath:   relativePath,
		Status:     "Failed",
		StatusCode: 0,
	}
	// Retrieve file info
	fileStat, err := os.Stat(path)
	if err != nil {
		return uploadResponse
	}

	fileContent, err := ioutil.ReadFile(path)
	// Return here if there is an error reading the file
	if err != nil {
		return uploadResponse
	}

	fileUploadBody := FileUploadMsg{
		IsDirectory:  fileStat.IsDir(),
		Mode:         uint(fileStat.Mode().Perm()),
		RelativePath: relativePath,
		Message:      "",
	}

	var buffer bytes.Buffer
	zWriter := zlib.NewWriter(&buffer)
	zWriter.Write([]byte(fileContent))

	zWriter.Close()
	encoded := base64.StdEncoding.EncodeToString(buffer.Bytes())
	fileUploadBody.Message = encoded

	buf := new(bytes.Buffer)
	json.NewEncoder(buf).Encode(fileUploadBody)

	projectUploadURL := conURL + "/api/v1/projects/" + projectID + "/upload"
	// TODO - How do we handle partial success?
	request, err := http.NewRequest("PUT", projectUploadURL, bytes.NewReader(buf.Bytes()))
	request.Header.Set("Content-Type", "application/json")
	resp, httpSecError := sechttp.DispatchHTTPRequest(client, request, connection)

	if httpSecError != nil {
		return uploadResponse
	}
	defer resp.Body.Close()
	return UploadedFile{
		FilePath:   relativePath,
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
	}
}
