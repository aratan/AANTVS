package p2p

import "encoding/json"

// ChunkRequest is sent by a peer requesting a video chunk.
type ChunkRequest struct {
	FileID    string `json:"file_id"`    // file name or hash
	ChunkIdx  int    `json:"chunk_idx"`  // which chunk (0-based)
	ChunkSize int    `json:"chunk_size"` // desired size (default 256KB)
}

// ChunkResponse is sent back with the chunk data.
type ChunkResponse struct {
	FileID    string `json:"file_id"`
	ChunkIdx  int    `json:"chunk_idx"`
	Data      []byte `json:"-"`          // raw binary, not JSON encoded
	Size      int    `json:"size"`
	TotalSize int64  `json:"total_size"`
	Error     string `json:"error,omitempty"`
}

// EncodeChunkRequest serializes a chunk request to JSON.
func EncodeChunkRequest(req ChunkRequest) ([]byte, error) {
	return json.Marshal(req)
}

// DecodeChunkRequest deserializes a chunk request from JSON.
func DecodeChunkRequest(data []byte) (ChunkRequest, error) {
	var req ChunkRequest
	err := json.Unmarshal(data, &req)
	return req, err
}
