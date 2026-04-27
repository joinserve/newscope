# ADR 0014 實作說明

交給實作者的開發描述。設計決定在 `docs/adr/0014-source-pivot-and-post-avatar.md`。
拆 2 個 PR：

- **PR 1 (Phase 1)** — 對應 ADR decisions 1-5。純 template/CSS/handler 改動，無 schema migration。
- **PR 2 (Phase 2)** — 對應 ADR decision 6。schema + parser + render，**動工前先做驗證**（見最下面）。

## 必讀先修

- `docs/adr/0014-source-pivot-and-post-avatar.md`（本次 6 個決定）
- `docs/adr/0011-beats-ui-design.md`（beats UI baseline、article-card 風格慣例）
- `docs/adr/0010-cross-item-deduplication-and-merge.md`（beats backend、`items.beat_id` 成員關係）
- `server/templates/article-card.html`（feed-card 要仿造的目標）
- `server/templates/beat-card.html`（avatar-stack 改動處）
- `server/templates/feed-card.html` / `feeds.html`（要改寫的對象）
- `pkg/repository/beat.go:625` `ListBeats`（要加 feed 過濾）
- `server/htmx_handlers.go:1373` `sourceHandler`（要從 articles 改成 beats）
- `server/htmx_handlers.go:402` `rsshubExplorerHandler`（要加 namespace search 端點）
- `server/static/css/style.css`（settings/feed-card/avatar-stack 都在這）

## PR 1 — UX 整批調整（Phase 1）

**目標**：5 個 UX 改動同一支 PR 上線，全部都能 revert by template/CSS。沒有 DB migration、沒有 worker 改動。建議實作順序就照下面 1.1 → 1.5（簡到難）。

### 1.1 Settings 列對齊

`server/static/css/style.css`（找 `.setting-item` block，目前在 ~line 798）：

```css
.setting-item-content {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
    align-items: flex-start;        /* 加這行 */
}
.setting-item-content label {
    margin: 0;                      /* 確保沒繼承到 indent */
}
```

實際打開 settings 頁檢查 — 如果加了 `align-items: flex-start` 還是偏右，往上翻看 `.settings-tab-content` / `.settings-page` 有沒有設 `padding-left` 或 `text-align: center`。應該不會，但要確認。

驗收：每一列的 label 與其他列的 label 上下對齊；value 仍靠右。

### 1.2 Feed-card 改 article-card 風格 + icon toolbar + 標題可點

#### 1.2.1 Template

`server/templates/feed-card.html` 全部重寫，仿 `server/templates/article-card.html` 的結構：

```html
<article class="feed-card article-card" data-feed-id="{{.ID}}">
    <div class="card-avatar">
        {{/* 沿用 article-card 的 favicon resolve 邏輯：先試 IconURL，否則用 google favicons */}}
        {{$iconSrc := ""}}
        {{if .IconURL}}
            {{if isImageURL .IconURL}}{{$iconSrc = .IconURL}}{{else}}{{$iconSrc = printf "https://www.google.com/s2/favicons?domain=%s&sz=128" .IconURL}}{{end}}
        {{else}}
            {{$domain := getDomain .URL}}
            {{if $domain}}{{$iconSrc = printf "https://www.google.com/s2/favicons?domain=%s&sz=128" $domain}}{{end}}
        {{end}}
        {{if $iconSrc}}
        <img src="{{$iconSrc}}" alt="{{.Title}}" class="avatar-img favicon-img" onerror="this.style.display='none'; this.nextElementSibling.style.display='flex';">
        <div class="avatar-img avatar-fallback" style="display:none;">{{slice .Title 0 1}}</div>
        {{else}}
        <div class="avatar-img avatar-fallback">{{slice .Title 0 1}}</div>
        {{end}}
    </div>
    <div class="card-main">
        <header class="card-head">
            <a href="/source/{{pathEscape .Title}}" class="feed-name">{{.Title}}</a>
            {{if not .Enabled}}<span class="feed-status-pill disabled">已停用</span>{{end}}
        </header>
        <p class="card-summary"><a href="{{.URL}}" target="_blank" rel="noopener">{{.URL}}</a></p>

        {{/* 編輯表單沿用既有展開／收合邏輯，只是放進來這個 card-main 裡 */}}
        <details class="feed-edit">
            <summary class="visually-hidden">編輯</summary>
            <form ...> ...原本的編輯欄位... </form>
        </details>

        <div class="card-actions" role="toolbar" aria-label="Feed actions">
            <button class="action action-edit" type="button" title="編輯"
                    onclick="this.closest('.feed-card').querySelector('.feed-edit').toggleAttribute('open')">
                <svg class="icon" ...><!-- pencil icon --></svg>
            </button>
            <button class="action action-toggle" type="button" title="{{if .Enabled}}停用{{else}}啟用{{end}}"
                    hx-post="/api/v1/feeds/{{.ID}}/toggle" hx-target="closest .feed-card" hx-swap="outerHTML">
                <svg class="icon" ...><!-- power icon --></svg>
            </button>
            <button class="action action-fetch" type="button" title="立即抓取"
                    hx-post="/api/v1/feeds/{{.ID}}/fetch" hx-target="closest .feed-card" hx-swap="outerHTML"
                    hx-indicator=".feed-fetch-indicator-{{.ID}}">
                <svg class="icon" ...><!-- refresh icon --></svg>
                <span class="feed-fetch-indicator-{{.ID}} htmx-indicator">…</span>
            </button>
            <button class="action action-delete" type="button" title="刪除"
                    hx-delete="/api/v1/feeds/{{.ID}}"
                    hx-confirm="確定要刪除此來源？"
                    hx-target="closest .feed-card" hx-swap="outerHTML swap:300ms">
                <svg class="icon" ...><!-- trash icon --></svg>
            </button>
        </div>
    </div>
</article>
```

不要改 endpoint。`hx-post`/`hx-delete` 對應的 URL 跟現在一樣。如果現在的 endpoint 路徑跟上面不一樣，照舊 endpoint 寫，**不要為了對齊樣板而改 server 路由**。

#### 1.2.2 CSS

`server/static/css/style.css`：

- `.feed-card` 直接套用 `.article-card` 的 grid 規則（`grid-template-columns: 48px 1fr`、padding、gap），新增規則只放差異點：
  ```css
  .feed-card .feed-status-pill { font-size: 0.7rem; ... }
  .feed-card .card-summary a { color: var(--text-secondary); }
  .feed-card .feed-edit[open] { ... }
  ```
- 移除舊的 `.feed-form` / `.btn-secondary` 在 feed 頁專用的版面規則（保留 settings 頁那邊）。
- 確保 mobile (`@media max-width: 768px`) 的 feed-card padding 與 article-card 一致。

#### 1.2.3 驗收

- `/feeds` 視覺上 feed-card 與 article-card 同款。
- 點 feed name → 跳到 `/source/{name}` （在 1.3 完成後就會跳到 beats by source）。
- Edit / 啟停 / Fetch / Delete 四個 icon 按鈕功能正常，hover 有提示。
- 行動裝置版面正常。

### 1.3 `/source/{name}` 改成 beats-by-source

#### 1.3.1 Repository — `ListBeats` 加 feed 過濾

`pkg/repository/beat.go:625` 的 `ListBeats(ctx, groupingID, topic, limit, offset)` 簽章改為：

```go
func (r *BeatRepo) ListBeats(ctx context.Context, opts ListBeatsOptions) ([]domain.BeatWithMembers, error)

type ListBeatsOptions struct {
    GroupingID *int64
    Topic      string
    FeedID     *int64   // 新增
    Limit      int
    Offset     int
}
```

（用 options struct 比硬加參數好，避免 callers 改太多次。如果為了 PR 體積考慮不想動 signature，就用 variadic option 或新增獨立的 `ListBeatsByFeed` — 兩個都可以，看實作者偏好，但**寫進 PR description 註明**。）

`FeedID` 用 `WHERE EXISTS`：

```sql
WHERE ...
  AND (? IS NULL OR EXISTS (
    SELECT 1 FROM items i WHERE i.beat_id = beats.id AND i.feed_id = ?
  ))
```

`items.beat_id` 已建索引（ADR 0010），效能 OK。

#### 1.3.2 `sourceHandler`

`server/htmx_handlers.go:1373` 重寫：

- 把目前兩段 `GetClassifiedItemsWithFilters` 拿掉。
- 改成單一 `s.db.GetFeedByName(ctx, feedName)` 拿 ID，然後 `s.db.ListBeats(ctx, ListBeatsOptions{FeedID: &id, Limit: 100})`。
- Render 邏輯沿用 beats list（呼叫既有 `s.renderBeatsListHTMX` 或直接用 `beats.html` 的 fragment）。
- `commonPageData.PageTitle` 維持 feed display name；`BackURL` 維持目前邏輯。
- HTMX 模式照 `beatsHandler` 的方式輸出 fragment + OOB title。

#### 1.3.3 Template

`server/templates/source.html` 改成 render beat-card 列表（直接 reuse `{{template "beat-card.html" .}}`）。可能整個檔案會變很短，類似：

```html
{{template "base.html" .}}
{{define "title"}}{{.FeedName}} - Newscope{{end}}
{{define "content"}}
<div id="articles-with-pagination">
    <div id="articles-container" class="view-threads">
        <div id="articles-list">
            {{range .Beats}}{{template "beat-card.html" .}}{{else}}<p class="no-articles">這個來源還沒有 beat。</p>{{end}}
        </div>
    </div>
</div>
{{end}}
```

舊 `unread-articles` / `read-articles` 兩段拿掉。

#### 1.3.4 Tests

- `pkg/repository/beat_test.go` 新增 `TestListBeats_FeedFilter`：建 2 個 feed 各一篇 item、各成一個 beat，用 `FeedID` 過濾應該只回傳一個。
- `server/htmx_handlers_test.go`（或新檔）測 `/source/{feed}` 回應包含對應 beat 的 ID、不包含其他 feed 的 beat。
- 既有 `sourceHandler` 測試會壞掉 — 改寫掉，不要 skip。

#### 1.3.5 驗收

- `/source/Threads` 顯示 Threads 為來源的 beat（不再是文章）。
- 從 beat-detail 點 username → 進到 `/source/{name}` → 看到該來源的 beats。
- 沒有 beat 時顯示 empty state，不是 500。

### 1.4 RSSHub namespace autocomplete

#### 1.4.1 Backend — 擴充 `/api/v1/rsshub/namespaces`

找到目前 namespaces 的 handler（grep `rsshub/namespaces`）。改成接受 `q` query param：

- 沒帶 `q`、沒帶 `category` → 回傳全部 namespace（cap 50）
- 只帶 `q` → 跨所有 namespace 模糊比對 `name` 跟 `key`（case-insensitive substring）
- 只帶 `category` → 維持現有行為
- 兩個都帶 → 兩個都要符合

回傳格式跟現在一樣（`[{name, key, url}]`），前端不用改 schema。

#### 1.4.2 Frontend — `rsshub-explorer.html`

在 categories view（step 1，現在 line ~99-103）上方加：

```html
<div class="rsshub-search-wrap">
    <input type="search" id="rsshub-namespace-search" placeholder="搜尋平台 (twitter, threads, ...)" autocomplete="off">
    <ul id="rsshub-search-suggestions" class="rsshub-suggestions" hidden></ul>
</div>
```

JS（加在現有 script tag 內）：

```js
(function() {
    const input = document.getElementById('rsshub-namespace-search');
    const list = document.getElementById('rsshub-search-suggestions');
    if (!input || !list) return;
    let timer = null;
    input.addEventListener('input', function() {
        clearTimeout(timer);
        const q = input.value.trim();
        if (q.length < 2) { list.hidden = true; list.innerHTML = ''; return; }
        timer = setTimeout(function() {
            fetch('/api/v1/rsshub/namespaces?q=' + encodeURIComponent(q))
                .then(r => r.json())
                .then(items => {
                    list.innerHTML = items.slice(0, 10).map(n =>
                        `<li data-key="${n.key}"><strong>${n.name}</strong> <span>${n.key}</span></li>`
                    ).join('');
                    list.hidden = items.length === 0;
                });
        }, 250);
    });
    list.addEventListener('click', function(e) {
        const li = e.target.closest('li[data-key]');
        if (!li) return;
        // reuse existing namespace navigation: 直接 call 現有 showNamespace(key) 或等價函式
        showNamespace(li.dataset.key);
        list.hidden = true;
        input.value = '';
    });
    document.addEventListener('click', function(e) {
        if (!list.contains(e.target) && e.target !== input) list.hidden = true;
    });
})();
```

`showNamespace(key)` 對應到目前 categories 點擊後跳到 routes view 的函式（找一下 `rsshub-explorer.html` 裡 click handler）。如果現有不是這個名字，直接用現有的，不要改。

#### 1.4.3 CSS

```css
.rsshub-search-wrap { position: relative; margin-bottom: 1rem; }
.rsshub-search-wrap input { width: 100%; ... }
.rsshub-suggestions {
    position: absolute; top: 100%; left: 0; right: 0;
    background: var(--bg-primary); border: 1px solid var(--border-primary);
    border-radius: 8px; max-height: 320px; overflow-y: auto;
    list-style: none; padding: 0.25rem 0; margin: 0; z-index: 10;
}
.rsshub-suggestions li { padding: 0.5rem 0.75rem; cursor: pointer; }
.rsshub-suggestions li:hover { background: var(--bg-hover); }
.rsshub-suggestions li span { color: var(--text-secondary); margin-left: 0.5rem; }
```

#### 1.4.4 驗收

- `/feeds/rsshub` 上方有搜尋框。
- 輸入 `tw` → autocomplete 顯示 Twitter / Twitch 等 namespace。
- 點任一筆 → 直接進到該 namespace 的 routes view，與點 category drill-down 結果相同。
- 沒有「搜尋結果頁」，dropdown 不會把使用者帶離 explorer。

### 1.5 Avatar-stack 簡化（1 + N）

`server/templates/beat-card.html`（line ~15-46，目前 multi-member 分支）：

```html
{{if gt (len .Members) 1}}
<div class="avatar-stack">
    {{$first := index .Members 0}}
    <!-- 沿用既有 favicon resolve 邏輯產出 first member 的 avatar -->
    <img src="..." class="avatar-img" alt="{{$first.FeedName}}" onerror="...">
    <span class="avatar-overflow">+{{add (len .Members) -1}}</span>
</div>
{{else}}
<!-- 單一 member 的分支不變 -->
{{end}}
```

CSS（找 `.avatar-stack` block，~line 2107）整段重寫：

```css
.avatar-stack {
    position: relative;
    width: 40px; height: 40px;
}
.avatar-stack .avatar-img {
    width: 40px; height: 40px;
    border-radius: 50%;
}
.avatar-stack .avatar-overflow {
    position: absolute;
    right: -4px; bottom: -4px;
    min-width: 22px; height: 22px;
    padding: 0 6px;
    border-radius: 11px;
    background: var(--text-primary);
    color: var(--bg-primary);
    font-size: 11px; font-weight: 700;
    display: flex; align-items: center; justify-content: center;
    border: 2px solid var(--bg-primary);
}
```

把舊的 `nth-child(2)` / `nth-child(3)` 規則砍掉。

驗收：3 來源的 beat 顯示「主 avatar + +2」，8 來源顯示「主 avatar + +7」，1 來源不變。

### 1.6 PR 1 完成檢查

- `go build ./...` 過
- `go test ./...` 過（含 1.3 新增的 ListBeats feed-filter 測試）
- `golangci-lint run` clean
- `gofmt -s -w` 已跑
- `unfuck-ai-comments run --fmt --skip=mocks` 已跑
- 手測：`/feeds`、`/source/{name}`、`/feeds/rsshub`、任一含 ≥3 sources 的 beat-card

---

## PR 2 — Feed.image_url + post avatar overlay（Phase 2）

**動工前先驗證**，見最末節「Phase 2 動工前驗證」。

### 2.1 Schema migration

`pkg/repository/schema.sql`：

```sql
ALTER TABLE feeds ADD COLUMN image_url TEXT NOT NULL DEFAULT '';
```

`pkg/repository/repository.go` 加 `migrateAddFeedImageURL`，仿 `migrateAddIconURL`（line 213）：

```go
func migrateAddFeedImageURL(ctx context.Context, db *sqlx.DB) error {
    has, err := columnExists(ctx, db, "feeds", "image_url")
    if err != nil { return err }
    if has { return nil }
    _, err = db.ExecContext(ctx, `ALTER TABLE feeds ADD COLUMN image_url TEXT NOT NULL DEFAULT ''`)
    return err
}
```

在 `Initialize` 裡 call（接在 `migrateAddIconURL` 之後）。

### 2.2 Domain + Repository 對應欄位

- `pkg/domain/feed.go`：`Feed` 加 `ImageURL string`。
- `pkg/repository/feed.go`：`feedSQL` 加 `ImageURL string \`db:"image_url"\``，`FromDomain` / `ToDomain` 對應；`UPDATE feeds SET ...` 不需要因為使用者不直接改這個欄位。
- 新增 method：`UpdateFeedImageURL(ctx context.Context, id int64, url string) error` — 寫一行 `UPDATE feeds SET image_url = ? WHERE id = ?`。

### 2.3 Parser 拉 channel image

`pkg/feed/parser.go`：`gofeed.Feed.Image` 是 `*Image{ URL, Title }`。Parse 後把 URL 帶出來。

可以擴 `ParseResult` 結構（如果有），讓 fetcher 能拿到。如果現在 parser 直接吐 items 就好沒帶 feed metadata，多回一個 `FeedImageURL string` 欄位。

### 2.4 Fetch worker 寫回

找 fetch worker（`pkg/scheduler/...`），抓完之後：

```go
if result.FeedImageURL != "" {
    if err := r.UpdateFeedImageURL(ctx, feedID, result.FeedImageURL); err != nil {
        log.Printf("[WARN] update feed image_url: %v", err)
    }
}
```

不要因為這個失敗就讓整個 fetch 失敗。

### 2.5 把 image_url 帶到 beat-card

ClassifiedItem 已有 `FeedIconURL`（`pkg/repository/classification.go:66`）。新增 `FeedImageURL string \`db:"feed_image_url"\``，在 `ListBeats` 跟 `GetBeat` 用到的 SELECT 裡都加 `f.image_url AS feed_image_url`（`pkg/repository/beat.go:807`、`pkg/repository/classification.go:112,192,662`）。

`pkg/domain/item.go` 的 `ClassifiedItem` 加 `FeedImageURL string`。

### 2.6 Beat-card render

`server/templates/beat-card.html`：avatar 區塊加判斷邏輯：

```html
{{$first := index .Members 0}}
{{$post := $first.FeedImageURL}}
{{$brand := $first.FeedIconURL}}
{{$hasOverlay := and $post (ne $post $brand)}}

<div class="card-avatar{{if $hasOverlay}} avatar-with-badge{{end}}">
    {{if $hasOverlay}}
    <img src="{{$post}}" class="avatar-img" alt="{{$first.FeedName}}" onerror="...">
    <img src="{{$brand}}" class="avatar-badge" alt="" aria-hidden="true">
    {{else}}
    <!-- 沿用 1.5 後的單 avatar / +N 分支 -->
    {{end}}
</div>
```

CSS：

```css
.card-avatar.avatar-with-badge { position: relative; }
.avatar-badge {
    position: absolute;
    right: -2px; bottom: -2px;
    width: 18px; height: 18px;
    border-radius: 50%;
    border: 2px solid var(--bg-primary);
    object-fit: cover;
    background: var(--bg-primary);
}
```

注意 1.5 的 `+N` 徽章也是放右下；如果同時有 overlay icon **跟** `+N`，優先 `+N`（badge 隱藏）。或者放兩個位置（左下 / 右下）— 看實際 render 效果決定，**寫進 PR description**。

### 2.7 Tests

- `pkg/feed/parser_test.go`：餵一個含 `<image><url>` 的 RSS 字串，assert `FeedImageURL` 被帶出來。
- `pkg/repository/feed_test.go`：`TestUpdateFeedImageURL` 新增 / 更新 / 空字串都 cover。
- `pkg/repository/beat_test.go`：assert `BeatWithMembers.Members[].FeedImageURL` 從 DB 帶出來。

### 2.8 PR 2 完成檢查

同 PR 1 + 額外：

- migration idempotent（重複 init 不會壞）。
- 至少 1 個 SNS feed 有 image_url 寫入 DB（手動拿 sqlite cli 驗）。
- beat-card 在 SNS beat 顯示 user avatar + 平台 icon overlay；非 SNS feed 沒 overlay、視覺等同 PR 1。

---

## Phase 2 動工前驗證

PR 2 寫之前，先用 `curl http://localhost:1200/<route>` 確認下列來源都會回傳 channel `<image><url>`：

- ✅ Threads — 已驗證（zuck）
- ⚠️ Twitter — 需要 RSSHub 設好 Twitter API（user 本機目前沒設），改到一個有設的 instance 確認
- ⚠️ Mastodon — 隨意挑一帳號（`/mastodon/timeline/Gargron@mastodon.social` 或類似）
- ⚠️ Bluesky / 其他常用 SNS

把驗證結果（哪些有、哪些沒）回報給 reviewer。沒返回 `<image>` 的來源 PR 2 不會 regress（fallback 到 icon_url），但要寫進 PR description「以下平台確認支援、以下平台不支援」。

---

## 一些細節提醒

- 不要動 `templates/base.html` 的 `page-title-content` block 邏輯，避免影響 ADR 0014 之外的頁面。
- PR 1 的 `/source/{name}` 改動會壞掉現有的 source page bookmark — **這在使用者預期內**（他主動要求改），不要為了相容性留 articles fallback。
- PR 2 的 ALTER TABLE 在 SQLite 上沒有 `IF NOT EXISTS`，要靠 `columnExists` 判斷，跟既有 migration 同模式。
- Commit message 一律小寫，PR description 不要含「Test plan」，符合 `CLAUDE.md` 既有規範。
