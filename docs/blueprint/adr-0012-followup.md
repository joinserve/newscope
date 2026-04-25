# ADR 0012 Follow-up Fixes

基於 `feat/topic-chip`（commit `1d71f03`）上的測試結果，有三個 bug 要修。

## 必讀

- `docs/adr/0012-topic-chip-inline-navigation.md`
- `docs/blueprint/adr-0012-implementation.md`（前一輪的 handoff）
- 本輪基底：branch `feat/topic-chip`，在這條 branch 上追加 commit

## Bug 1 — 藍色 chip 永遠不會藍（CSS 特異性）

**現象：** `/beats` 和 `/articles` 的 header 「› #topic」chip 應該是藍底白字，實際是灰底。

**根因：** `server/static/css/style.css`
- L1342 `.card-head .card-topic { background-color: var(--bg-tertiary); ... }` 特異性 (0,2,0)
- L1613 `.topic-chip-big { background: var(--accent-blue, #3b82f6); ... }` 特異性 (0,1,0)
- 前者覆蓋後者，header 裡的 chip 永遠灰色

**修法：** 在 `.topic-chip-big` 之後新增一條特異性更高的規則：
```css
.card-head .card-topic.topic-chip-big {
    background: var(--accent-blue, #3b82f6);
    color: white;
}
```

底部 `.topics` 那排不在 `.card-head` 內，原本的 `.topic-chip-big` 就夠。

## Bug 2 — 單一 member beat 的 PrimaryTopic 退化成「LLM 排序」

**現象：** 一張只有 1 個 member 的 beat（topics: `["security","china","surveillance"]`）顯示 `#security` 為 primary，但 `china`（全域 130 篇）明顯比 `security`（50 篇）大很多。

**根因：** `pkg/domain/beat.go` 的 `PrimaryTopic()`
- 現有邏輯：統計所有 members 的 topic 出現次數，tie 時取「第一個遇到」
- 對單 member beat：所有 topic count=1 → tie → 永遠取 LLM 原始排序的第一個
- 失去「突出重點 topic」的語意

**修法：** tie-break 從「first occurrence」改成「全域計數最大」。

`PrimaryTopic` 要能拿到 big-tag counts。建議改 signature：

```go
// pkg/domain/beat.go
func (b *BeatWithMembers) PrimaryTopic(globalCounts map[string]int) string
```

- 先統計 member 內 count（同現行）
- 取 member 內 count 最高的 topic 集合
- 集合大小 > 1 時，用 `globalCounts` 拆 tie，取全域最大
- `globalCounts` 為 nil 或查不到時，退回「first occurrence」

`globalCounts` 來源：server 層的 `bigTags.tags` 目前只存 set（`map[string]struct{}`），需要改存 `map[string]int`（count）。`refreshBigTags` 已經從 DB 拿到 counts，直接存下即可；同時更新 `isBigTag` funcMap closure 判斷方式（`_, ok := cache.tags[tag]` 改成 `cache.tags[tag] > 0` 或類似）。

Template 呼叫：beat-card.html / beat-detail.html 的 `.PrimaryTopic` 改成透過 funcMap helper：

```go
"beatPrimaryTopic": func(b *domain.BeatWithMembers) string {
    cache.mu.RLock()
    defer cache.mu.RUnlock()
    return b.PrimaryTopic(cache.tags)  // tags 改存 count
}
```

Template：`{{beatPrimaryTopic .}}` 取代 `.PrimaryTopic`。

（或保留 `PrimaryTopic()` 無參數版本作為 fallback，新增帶參數的 `PrimaryTopicWithCounts()` — 看你偏好。）

## Bug 3 — Beat-card 底部 `.topics` 沒有大 tag 樣式

**現象：** `server/templates/beat-card.html` L132-145 底部 `.topics` 迴圈，每個 topic 都只有 `topic-tag clickable-topic` class，沒有 `#` 前綴、沒有 `topic-chip-big`。Article-card 底部 `.topics`（L90-104）則有。

**修法：** 對齊 article-card 的寫法：

```html
{{if .Topics}}
<div class="topics">
    {{range .Topics}}
    <a href="#" class="topic-tag clickable-topic {{if isBigTag .}}topic-chip-big{{end}}"
       data-topic="{{.}}"
       hx-get="/beats"
       hx-vals='{"topic":"{{.}}"}'
       hx-push-url="true"
       hx-trigger="click"
       hx-target="#articles-with-pagination"
       hx-swap="innerHTML show:body:top"
       hx-include="#score-filter, #feed-filter, #sort-filter, #date-range-filter">{{if isBigTag .}}#{{end}}{{.}}</a>
    {{end}}
</div>
{{end}}
```

注意：beat-card 沒有 article-card 的 clear-filter 邏輯（`{{if ne $.SelectedTopic .}}hx-vals=...{{end}}`），這點**先不補**（前一輪實作者已決策，beat-card 目前沒有 `SelectedTopic` 欄位，補起來 scope 會擴大到 handler）。

## Tests

- `TestBeatWithMembers_PrimaryTopic` 加 case：單 member、多 topic、tie 靠 `globalCounts` 判斷
- Handler 測試：verify beat-card 底部 `.topics` 含 `topic-chip-big` class（大 tag case）
- CSS 無單元測試；手動驗證 `/beats` 和 `/articles` header chip 是藍底白字

## 驗收

- `/beats` 某張 CNA 單 member beat（topics 含 `china`）→ header chip 顯示 `#china`（全域最大的那個），藍底白字
- `/beats` 底部 `.topics` 三個 tag 都顯示 `#tag` + 藍底（全都過 threshold 的話）
- `/articles` header 「› #topic」chip 藍底白字
- build / test / lint 全綠

## 注意事項

- 不要跑 `unfuck-ai-comments 整庫掃過`
- Commit 用 lowercase
- 不要加 Co-Authored-By / Generated with Claude Code trailer
- Git author 若是 `Cluade`（typo），用 `git -c user.name="Claude" commit ...` 覆寫
- PR description 不要加 Test plan 章節
- 在 `feat/topic-chip` 分支上 append commit，不要 rebase 或 squash 掉 `1d71f03`
