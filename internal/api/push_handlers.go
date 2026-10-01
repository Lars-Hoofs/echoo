package api

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/auth"
	"echoo/internal/db/dbq"
	"echoo/internal/push"
)

const maxEndpoint = 2048

var (
	apnsToken = regexp.MustCompile(`^[0-9a-f]{64,200}$`)
	fcmToken  = regexp.MustCompile(`^[A-Za-z0-9_:\-]+$`)
)

func (s *Server) pushRoutes(r chi.Router) {
	testLimiter := auth.NewLimiter(10, time.Minute)
	r.Get("/push/config", s.pushConfig)
	r.Get("/push/devices", s.listPushDevices)
	// A device belongs to the session that registered it, so API tokens cannot add one.
	r.With(sessionOnly).Post("/push/devices", s.registerPushDevice)
	r.With(sessionOnly).Delete("/push/devices/{id}", s.deletePushDevice)
	r.With(limitByUser(testLimiter)).Post("/push/test", s.testPush)
}

// pushConfig tells the client which kinds of device it can register.
func (s *Server) pushConfig(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"web_push_public_key": nil, "apns": s.push.APNs != nil, "fcm": s.push.FCM != nil}
	if s.push.Web != nil {
		out["web_push_public_key"] = base64.RawURLEncoding.EncodeToString(s.push.Web.PublicKey())
	}
	writeJSON(w, http.StatusOK, out)
}

type pushDeviceJSON struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	LastPushAt *time.Time `json:"last_push_at"`
	// Current is true for the device registered by the session asking.
	Current bool `json:"current"`
}

func (s *Server) listPushDevices(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	rows, err := s.q.PushListDevices(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]pushDeviceJSON, len(rows))
	for i, d := range rows {
		out[i] = pushDeviceJSON{
			ID: uuidStr(d.ID), Kind: d.Kind, Label: d.Label, CreatedAt: d.CreatedAt.Time.UTC(),
			LastPushAt: timeOrNil(d.LastPushAt), Current: d.SessionID.Valid && d.SessionID == sess.ID,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func decodeB64URL(v string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		// Browsers hand out unpadded base64url, but some wrappers pad it.
		b, err = base64.URLEncoding.DecodeString(v)
	}
	if err != nil {
		return nil
	}
	return b
}

func (s *Server) registerPushDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string `json:"kind"`
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Label string `json:"label"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	var p256dh, authSecret []byte
	// The column holds 2048 characters; FCM tokens and push service URLs stay far below.
	if len(req.Endpoint) > maxEndpoint {
		fields["endpoint"] = "invalid"
	}
	switch req.Kind {
	case push.KindWebPush:
		if s.push.Web == nil {
			fields["kind"] = "not_configured"
			break
		}
		if err := push.ValidWebPushEndpoint(req.Endpoint); err != nil {
			fields["endpoint"] = "invalid"
		}
		p256dh, authSecret = decodeB64URL(req.Keys.P256dh), decodeB64URL(req.Keys.Auth)
		if len(p256dh) != 65 || p256dh[0] != 4 {
			fields["keys.p256dh"] = "invalid"
		}
		if len(authSecret) != 16 {
			fields["keys.auth"] = "invalid"
		}
	case push.KindAPNs:
		switch {
		case s.push.APNs == nil:
			fields["kind"] = "not_configured"
		case !apnsToken.MatchString(req.Endpoint):
			fields["endpoint"] = "invalid"
		}
	case push.KindFCM:
		switch {
		case s.push.FCM == nil:
			fields["kind"] = "not_configured"
		case len(req.Endpoint) < 20 || !fcmToken.MatchString(req.Endpoint):
			fields["endpoint"] = "invalid"
		}
	default:
		fields["kind"] = "invalid"
	}
	label := ""
	if req.Label != "" {
		var ok bool
		if label, ok = cleanText(req.Label, 200); !ok {
			fields["label"] = "invalid"
		}
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	sess := sessionFrom(r.Context())
	d, err := s.q.PushUpsertDevice(r.Context(), dbq.PushUpsertDeviceParams{
		UserID: sess.User.ID, SessionID: sess.ID, Kind: req.Kind, Endpoint: req.Endpoint,
		P256dh: nonNilBytes(p256dh), Auth: nonNilBytes(authSecret), Label: label,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pushDeviceJSON{
		ID: uuidStr(d.ID), Kind: d.Kind, Label: d.Label, CreatedAt: d.CreatedAt.Time.UTC(), LastPushAt: timeOrNil(d.LastPushAt), Current: true,
	})
}

func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func (s *Server) deletePushDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	n, err := s.q.PushDeleteDevice(r.Context(), dbq.PushDeleteDeviceParams{ID: id, UserID: sessionFrom(r.Context()).User.ID})
	switch {
	case err != nil:
		writeError(w, r, err)
	case n == 0:
		writeError(w, r, errNotFound)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// testPush sends a test notification to every device of the caller right away, so an agent
// can check that the phone will ring before it matters.
func (s *Server) testPush(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	devices, err := s.q.PushDevicesOfUsers(r.Context(), []pgtype.UUID{user.ID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	m := push.Message{Title: "Testmelding van Echoo", Body: "Pushmeldingen werken op dit apparaat.", URL: "/instellingen/meldingen", Tag: "test"}
	type result struct {
		ID    string `json:"id"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	out := make([]result, 0, len(devices))
	for _, d := range devices {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err := s.push.Send(ctx, push.Device{ID: d.ID, Kind: d.Kind, Endpoint: d.Endpoint, P256dh: d.P256dh, Auth: d.Auth}, m, m)
		cancel()
		res := result{ID: uuidStr(d.ID), OK: err == nil}
		switch {
		case errors.Is(err, push.ErrGone):
			res.Error = "gone"
			if err := s.q.PushDropDevice(r.Context(), d.ID); err != nil {
				writeError(w, r, err)
				return
			}
		case err != nil:
			res.Error = "failed"
			slog.WarnContext(r.Context(), "test push failed", "kind", d.Kind, "err", err, "request_id", requestIDFrom(r.Context()))
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}
