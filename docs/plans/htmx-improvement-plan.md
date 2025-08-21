# HTMX Implementation Improvement Plan

## Overview
This plan outlines the systematic improvement of the newscope application to achieve a truly server-driven architecture using HTMX, eliminating JavaScript dependencies where possible.

## Priority Matrix

### High Priority (Critical Path - Week 1)
These changes remove the most JavaScript and establish foundational patterns.

#### 1. Server-Side Session Management for User Preferences
**Effort:** Medium (4-6 hours)
**Impact:** Eliminates all localStorage usage
**Dependencies:** None

**Changes Required:**

1. **Backend (Go)**
   - Add session middleware to store user preferences
   - Create preference endpoints
   - Modify handlers to read from session

   ```go
   // internal/web/preference_handlers.go (NEW)
   type UserPreferences struct {
       Theme    string `json:"theme"`
       ViewMode string `json:"viewMode"`
       ShowSummary bool `json:"showSummary"`
   }

   func (h *Handlers) SetPreference(w http.ResponseWriter, r *http.Request) {
       pref := r.FormValue("preference")
       value := r.FormValue("value")
       
       session := h.getSession(r)
       session.Values[pref] = value
       session.Save(r, w)
       
       // Return appropriate UI update based on preference
       switch pref {
       case "theme":
           h.renderThemeToggle(w, r, value)
       case "viewMode":
           h.renderNewsList(w, r) // Re-render with new mode
       }
   }
   ```

2. **Frontend Templates**
   
   **BEFORE (templates/partials/header.html):**
   ```html
   <button id="theme-toggle" onclick="toggleTheme()" class="btn btn-ghost btn-sm">
       <span class="theme-icon">🌙</span>
   </button>
   ```

   **AFTER:**
   ```html
   <button hx-post="/api/preferences/theme" 
           hx-trigger="click"
           hx-target="body"
           hx-swap="outerHTML"
           class="btn btn-ghost btn-sm">
       <span class="theme-icon">{{if eq .Theme "dark"}}☀️{{else}}🌙{{end}}</span>
   </button>
   ```

#### 2. Convert Theme Toggle to Pure HTMX
**Effort:** Low (2-3 hours)
**Impact:** Removes 30+ lines of JavaScript
**Dependencies:** Task 1

**Changes Required:**

1. **Backend:**
   ```go
   // internal/web/handlers.go
   func (h *Handlers) ToggleTheme(w http.ResponseWriter, r *http.Request) {
       session := h.getSession(r)
       currentTheme := session.Values["theme"].(string)
       
       newTheme := "light"
       if currentTheme == "light" {
           newTheme = "dark"
       }
       
       session.Values["theme"] = newTheme
       session.Save(r, w)
       
       // Return full page with new theme
       w.Header().Set("HX-Trigger", "themeChanged")
       h.renderFullPage(w, r, newTheme)
   }
   ```

2. **Remove JavaScript (templates/layout.html):**
   ```html
   <!-- DELETE THIS ENTIRE SCRIPT BLOCK -->
   <script>
       function toggleTheme() { ... }
       // All theme-related JS
   </script>
   ```

#### 3. Convert Search Toggle to HTMX
**Effort:** Low (2 hours)
**Impact:** Removes search toggle JavaScript
**Dependencies:** None

**BEFORE (templates/partials/header.html):**
```html
<button onclick="toggleSearch()" class="btn btn-ghost btn-sm">
    🔍 Search
</button>
<script>
    function toggleSearch() {
        document.getElementById('search-container').classList.toggle('hidden');
    }
</script>
```

**AFTER:**
```html
<button hx-get="/partials/search-toggle" 
        hx-target="#search-container"
        hx-swap="outerHTML"
        class="btn btn-ghost btn-sm">
    🔍 Search
</button>
<div id="search-container" class="{{if not .ShowSearch}}hidden{{end}}">
    <!-- search form -->
</div>
```

**Backend:**
```go
func (h *Handlers) ToggleSearch(w http.ResponseWriter, r *http.Request) {
    session := h.getSession(r)
    showSearch := !session.Values["showSearch"].(bool)
    session.Values["showSearch"] = showSearch
    
    // Render search container with appropriate visibility
    h.renderTemplate(w, "partials/search.html", map[string]interface{}{
        "ShowSearch": showSearch,
    })
}
```

### Medium Priority (Enhanced Functionality - Week 2)

#### 4. Implement SSE for Real-time Updates
**Effort:** Medium (4-5 hours)
**Impact:** Adds real-time capabilities without JavaScript
**Dependencies:** None

**Changes Required:**

1. **Backend SSE Handler:**
   ```go
   // internal/web/sse_handler.go (NEW)
   func (h *Handlers) SSEHandler(w http.ResponseWriter, r *http.Request) {
       w.Header().Set("Content-Type", "text/event-stream")
       w.Header().Set("Cache-Control", "no-cache")
       w.Header().Set("Connection", "keep-alive")
       
       flusher := w.(http.Flusher)
       
       for {
           select {
           case update := <-h.updates:
               fmt.Fprintf(w, "event: news-update\n")
               fmt.Fprintf(w, "data: <div hx-swap-oob='afterbegin:#news-list'>%s</div>\n\n", update)
               flusher.Flush()
           case <-r.Context().Done():
               return
           }
       }
   }
   ```

2. **Frontend Template:**
   ```html
   <div hx-ext="sse" 
        sse-connect="/api/sse"
        sse-swap="news-update"
        id="news-list">
       <!-- News items -->
   </div>
   ```

#### 5. View Mode Switching (Expanded/Condensed)
**Effort:** Low (2-3 hours)
**Impact:** Removes view mode JavaScript
**Dependencies:** Task 1

**BEFORE:**
```html
<button onclick="toggleViewMode()" class="btn btn-sm">
    Toggle View
</button>
<script>
    function toggleViewMode() {
        // JavaScript logic
    }
</script>
```

**AFTER:**
```html
<div id="view-controls">
    <button hx-post="/api/preferences/viewMode" 
            hx-vals='{"mode":"{{if eq .ViewMode "expanded"}}condensed{{else}}expanded{{end}}"}'
            hx-target="#news-container"
            hx-swap="outerHTML"
            class="btn btn-sm">
        {{if eq .ViewMode "expanded"}}📰 Condensed{{else}}📄 Expanded{{end}}
    </button>
</div>
```

#### 6. Convert Summary Toggle to HTMX
**Effort:** Low (2 hours)
**Impact:** Removes inline onclick handlers
**Dependencies:** Task 1

**BEFORE:**
```html
<button onclick="toggleSummary('{{.ID}}')" class="btn btn-xs">
    Show Summary
</button>
```

**AFTER:**
```html
<button hx-get="/api/news/{{.ID}}/summary" 
        hx-target="#summary-{{.ID}}"
        hx-swap="outerHTML"
        hx-indicator="#spinner-{{.ID}}"
        class="btn btn-xs">
    Show Summary
</button>
<div id="summary-{{.ID}}"></div>
<span id="spinner-{{.ID}}" class="htmx-indicator">Loading...</span>
```

#### 7. Add Loading States and Indicators
**Effort:** Low (2 hours)
**Impact:** Better UX without JavaScript
**Dependencies:** None

**Implementation:**
```html
<!-- Global loading indicator -->
<div id="global-indicator" class="htmx-indicator">
    <div class="spinner"></div>
</div>

<!-- Per-request indicators -->
<form hx-post="/api/search" 
      hx-indicator="#search-spinner"
      hx-target="#results">
    <input name="q" type="search">
    <button type="submit">Search</button>
    <span id="search-spinner" class="htmx-indicator">Searching...</span>
</form>
```

**CSS:**
```css
.htmx-indicator {
    display: none;
}
.htmx-request .htmx-indicator {
    display: inline-block;
}
.htmx-request.htmx-indicator {
    display: inline-block;
}
```

### Low Priority (Polish & Optimization - Week 3)

#### 8. Implement Debounced Search
**Effort:** Low (1-2 hours)
**Impact:** Better search UX
**Dependencies:** Task 3

**Implementation:**
```html
<input type="search" 
       name="q"
       hx-get="/api/search"
       hx-trigger="keyup changed delay:500ms, search"
       hx-target="#search-results"
       hx-indicator="#search-indicator"
       placeholder="Search news...">
```

#### 9. Add Out-of-Band Updates
**Effort:** Medium (3-4 hours)
**Impact:** Enables multiple UI updates from single request
**Dependencies:** None

**Example Implementation:**
```go
// Backend - return multiple updates
func (h *Handlers) UpdateNews(w http.ResponseWriter, r *http.Request) {
    // Main content
    fmt.Fprintf(w, `<div id="news-item">%s</div>`, newsHTML)
    
    // Out-of-band updates
    fmt.Fprintf(w, `<div id="news-count" hx-swap-oob="true">Total: %d</div>`, count)
    fmt.Fprintf(w, `<div id="last-updated" hx-swap-oob="true">Updated: %s</div>`, time.Now())
}
```

#### 10. Implement Infinite Scroll
**Effort:** Low (2 hours)
**Impact:** Better pagination UX
**Dependencies:** None

**Implementation:**
```html
<div id="news-list">
    {{range .NewsItems}}
        <!-- news items -->
    {{end}}
    
    <div hx-get="/api/news?page={{.NextPage}}"
         hx-trigger="revealed"
         hx-target="#news-list"
         hx-swap="beforeend"
         hx-indicator="#load-more-spinner">
        <span id="load-more-spinner" class="htmx-indicator">Loading more...</span>
    </div>
</div>
```

#### 11. Add History Support
**Effort:** Low (2 hours)
**Impact:** Better navigation experience
**Dependencies:** None

**Implementation:**
```html
<!-- Enable history for navigation -->
<a href="/category/tech" 
   hx-get="/api/category/tech"
   hx-target="#content"
   hx-push-url="true"
   hx-swap="innerHTML">
   Technology
</a>
```

#### 12. Implement Form Validation
**Effort:** Medium (3 hours)
**Impact:** Better form UX
**Dependencies:** None

**Implementation:**
```html
<form hx-post="/api/submit" 
      hx-target="#form-container"
      hx-swap="outerHTML">
    <input name="email" 
           type="email"
           hx-post="/api/validate/email"
           hx-trigger="blur changed delay:500ms"
           hx-target="next .error"
           required>
    <span class="error"></span>
    <button type="submit">Submit</button>
</form>
```

## Implementation Order & Timeline

### Week 1: Foundation (High Priority)
1. Day 1-2: Server-side session management (#1)
2. Day 2-3: Theme toggle conversion (#2)
3. Day 3-4: Search toggle conversion (#3)
4. Day 5: Testing and bug fixes

### Week 2: Core Features (Medium Priority)
1. Day 1-2: SSE implementation (#4)
2. Day 2-3: View mode switching (#5)
3. Day 3-4: Summary toggle (#6)
4. Day 4-5: Loading states (#7)
5. Day 5: Integration testing

### Week 3: Polish (Low Priority)
1. Day 1: Debounced search (#8)
2. Day 2: Out-of-band updates (#9)
3. Day 3: Infinite scroll (#10)
4. Day 4: History support (#11)
5. Day 5: Form validation (#12)

## Backend Architecture Changes

### 1. Session Store
```go
// internal/session/store.go (NEW)
type SessionStore struct {
    store *sessions.CookieStore
}

func NewSessionStore(secret []byte) *SessionStore {
    return &SessionStore{
        store: sessions.NewCookieStore(secret),
    }
}
```

### 2. Preference Service
```go
// internal/service/preferences.go (NEW)
type PreferenceService struct {
    sessionStore *SessionStore
}

func (s *PreferenceService) GetPreferences(r *http.Request) UserPreferences {
    // Get from session
}

func (s *PreferenceService) SetPreference(r *http.Request, key, value string) error {
    // Set in session
}
```

### 3. Template Helpers
```go
// internal/web/template_helpers.go
func (h *Handlers) renderWithPreferences(w http.ResponseWriter, tmpl string, data interface{}, r *http.Request) {
    prefs := h.preferenceService.GetPreferences(r)
    
    templateData := map[string]interface{}{
        "Data": data,
        "Preferences": prefs,
    }
    
    h.renderTemplate(w, tmpl, templateData)
}
```

## Testing Strategy

### 1. Unit Tests
- Test preference storage/retrieval
- Test SSE event handling
- Test form validation logic

### 2. Integration Tests
```go
func TestThemeToggle(t *testing.T) {
    // Test theme switching via HTMX endpoint
    req := httptest.NewRequest("POST", "/api/preferences/theme", nil)
    // Assert response contains correct theme
}
```

### 3. HTMX-Specific Tests
```go
func TestHTMXHeaders(t *testing.T) {
    req := httptest.NewRequest("GET", "/api/news", nil)
    req.Header.Set("HX-Request", "true")
    // Assert response is fragment, not full page
}
```

## Migration Checklist

- [ ] Set up session management
- [ ] Create preference endpoints
- [ ] Remove all localStorage references
- [ ] Convert theme toggle
- [ ] Convert search toggle
- [ ] Remove all onclick handlers
- [ ] Remove all JavaScript functions
- [ ] Add SSE support
- [ ] Implement loading indicators
- [ ] Add form validation
- [ ] Update documentation
- [ ] Run full test suite
- [ ] Performance testing

## Success Metrics

1. **JavaScript Reduction:** From ~200 lines to <20 lines (only HTMX config)
2. **Response Time:** All interactions <200ms
3. **Browser Compatibility:** Works without JavaScript enabled
4. **Code Simplification:** 30% reduction in frontend complexity
5. **Maintainability:** All logic server-side

## Potential Challenges & Solutions

### Challenge 1: Session Management Scale
**Solution:** Use Redis for session storage if scaling beyond single server

### Challenge 2: SSE Browser Limits
**Solution:** Implement connection pooling and automatic reconnection

### Challenge 3: SEO Impact
**Solution:** Ensure all content is server-rendered on initial load

## Notes

- All JavaScript removal should be done incrementally
- Keep a minimal fallback for critical features during transition
- Test each change thoroughly before moving to next
- Document any remaining JavaScript with clear justification