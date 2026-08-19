package watchd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
)

const (
	cmdSnapshot = "snapshot"
	cmdPing     = "ping"
)

type wireRequest struct {
	Cmd   string   `json:"cmd"`
	Roots []string `json:"roots,omitempty"`
}

type wireResponse struct {
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

func sendRequest(conn net.Conn, req wireRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

func receiveRequest(conn net.Conn) (wireRequest, error) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return wireRequest{}, err
		}
		return wireRequest{}, fmt.Errorf("connection closed")
	}
	var req wireRequest
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		return wireRequest{}, fmt.Errorf("unmarshal request: %w", err)
	}
	return req, nil
}

func sendResponse(conn net.Conn, resp wireResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

func receiveResponse(conn net.Conn) (wireResponse, error) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return wireResponse{}, err
		}
		return wireResponse{}, fmt.Errorf("connection closed")
	}
	var resp wireResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return wireResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return resp, nil
}
