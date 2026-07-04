package server

import (
	"net/http"
	"server/api"

	"google.golang.org/protobuf/proto"
)

func (s *Server) getQuotes(w http.ResponseWriter, body []byte) error {
	var req api.QuotesRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return err
	}

	return s.response(w, &api.QuotesResponse{})
}
