package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/umputun/newscope/pkg/domain"
)

// groupingsSettingsHandler renders the groupings management page.
func (s *Server) groupingsSettingsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupings, err := s.db.ListGroupings(ctx)
	if err != nil {
		log.Printf("[ERROR] list groupings: %v", err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to load groupings", err)
		return
	}

	topics, _ := s.db.GetTopicsFiltered(ctx, 0.0)
	activeFeeds, _ := s.db.GetActiveFeedNames(ctx, 0.0)

	data := struct {
		commonPageData
		Groupings []domain.Grouping
	}{
		commonPageData: commonPageData{
			ActivePage:   "settings",
			PageTitle:    "Groupings",
			BackURL:      "/settings",
			FilterTopics: topics,
			FilterFeeds:  activeFeeds,
		},
		Groupings: groupings,
	}

	if err := s.renderPage(w, "groupings.html", data); err != nil {
		s.respondWithError(w, http.StatusInternalServerError, "Failed to render page", err)
	}
}

// createGroupingHandler handles POST /api/v1/groupings.
// Accepts form fields: name, tags (comma-separated string).
func (s *Server) createGroupingHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid form data", err)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.respondWithError(w, http.StatusBadRequest, "name is required", nil)
		return
	}
	tags := parseTags(r.FormValue("tags"))

	id, err := s.db.CreateGrouping(ctx, domain.Grouping{Name: name, Tags: tags})
	if err != nil {
		log.Printf("[ERROR] create grouping: %v", err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to create grouping", err)
		return
	}

	groupings, err := s.db.ListGroupings(ctx)
	if err != nil {
		log.Printf("[ERROR] list groupings after create: %v", err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to reload groupings", err)
		return
	}

	log.Printf("[INFO] created grouping id=%d name=%q", id, name) //nolint:gosec // %q quotes the value
	s.triggerReassignAll()
	s.renderGroupingsList(w, r, groupings)
}

// updateGroupingHandler handles PUT /api/v1/groupings/{id}.
func (s *Server) updateGroupingHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid grouping ID", err)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid form data", err)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.respondWithError(w, http.StatusBadRequest, "name is required", nil)
		return
	}
	tags := parseTags(r.FormValue("tags"))

	if err := s.db.UpdateGrouping(ctx, domain.Grouping{ID: id, Name: name, Tags: tags}); err != nil {
		log.Printf("[ERROR] update grouping %d: %v", id, err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to update grouping", err)
		return
	}

	groupings, err := s.db.ListGroupings(ctx)
	if err != nil {
		log.Printf("[ERROR] list groupings after update: %v", err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to reload groupings", err)
		return
	}

	s.triggerReassignAll()
	s.renderGroupingsList(w, r, groupings)
}

// deleteGroupingHandler handles DELETE /api/v1/groupings/{id}.
func (s *Server) deleteGroupingHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid grouping ID", err)
		return
	}

	if err := s.db.DeleteGrouping(ctx, id); err != nil {
		log.Printf("[ERROR] delete grouping %d: %v", id, err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to delete grouping", err)
		return
	}

	groupings, err := s.db.ListGroupings(ctx)
	if err != nil {
		log.Printf("[ERROR] list groupings after delete: %v", err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to reload groupings", err)
		return
	}

	s.triggerReassignAll()
	s.renderGroupingsList(w, r, groupings)
}

// reorderGroupingsHandler handles POST /api/v1/groupings/reorder.
// Accepts JSON body: {"ids": [3, 1, 2]}.
func (s *Server) reorderGroupingsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid JSON body", err)
		return
	}

	if err := s.db.ReorderGroupings(ctx, body.IDs); err != nil {
		log.Printf("[ERROR] reorder groupings: %v", err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to reorder", err)
		return
	}

	groupings, err := s.db.ListGroupings(ctx)
	if err != nil {
		log.Printf("[ERROR] list groupings after reorder: %v", err)
		s.respondWithError(w, http.StatusInternalServerError, "Failed to reload groupings", err)
		return
	}

	s.triggerReassignAll()
	s.renderGroupingsList(w, r, groupings)
}

// groupingEditFormHandler handles GET /settings/groupings/{id}/edit.
// Returns the inline edit form partial for the given grouping.
func (s *Server) groupingEditFormHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid grouping ID", err)
		return
	}

	g, err := s.db.GetGrouping(ctx, id)
	if err != nil {
		log.Printf("[ERROR] get grouping %d: %v", id, err)
		s.respondWithError(w, http.StatusNotFound, "Grouping not found", err)
		return
	}

	if err := s.templates.ExecuteTemplate(w, "grouping-edit-form", g); err != nil {
		log.Printf("[WARN] failed to render edit form: %v", err)
	}
}

// renderGroupingsList renders the groupings-list partial.
func (s *Server) renderGroupingsList(w http.ResponseWriter, _ *http.Request, groupings []domain.Grouping) {
	if err := s.templates.ExecuteTemplate(w, "groupings-list.html", groupings); err != nil {
		log.Printf("[WARN] failed to render groupings list: %v", err)
	}
}

// triggerReassignAll fires a full reassignment in the background so CRUD responses
// are not delayed. Uses a detached context so the goroutine outlives the HTTP request.
func (s *Server) triggerReassignAll() {
	if s.groupingEngine == nil {
		return
	}
	go func() {
		if err := s.groupingEngine.ReassignAll(context.Background(), 48*time.Hour); err != nil {
			log.Printf("[WARN] grouping reassign all: %v", err)
		}
	}()
}

// suggestTagsHandler handles GET /api/v1/tags/suggest?q=<prefix>.
// Returns HTML <option> elements for a <datalist> autocomplete widget.
func (s *Server) suggestTagsHandler(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimSpace(r.URL.Query().Get("q"))
	tags, err := s.db.SuggestTags(r.Context(), prefix, 50)
	if err != nil {
		log.Printf("[WARN] suggest tags: %v", err)
		tags = nil
	}
	w.Header().Set("Content-Type", "text/html")
	for _, t := range tags {
		fmt.Fprintf(w, "<option value=%q>\n", html.EscapeString(t))
	}
}

// parseTags splits a comma-separated tag string into a slice; ignores blank entries.
func parseTags(raw string) []string {
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

