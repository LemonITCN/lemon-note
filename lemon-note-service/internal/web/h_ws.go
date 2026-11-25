package web

import (
	"encoding/json"
	"net/http"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/realtime"
)

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	a := AuthOf(r)
	if a == nil || a.Session == nil {
		s.writeError(w, r, errUnauth())
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !sameHost(origin, r.Host) {
		s.writeError(w, r, errForbidden())
		return
	}
	s.Hub.Serve(w, r, a.User.ID, func(conn *realtime.Conn, typ string, data json.RawMessage) {
		if h := s.WSHandlers[typ]; h != nil {
			h(conn, data)
		}
	})
}
