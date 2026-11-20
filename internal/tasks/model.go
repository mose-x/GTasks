package tasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// OpSnapshot is the read side of the client: a single RPC that returns every
// list plus every task the server is willing to send in one shot.
//
// The web app never issues another request when the user switches lists, opens
// 已加星标 or expands 已完成 — all of those views are rendered locally from this
// one payload. Snapshotting it therefore reproduces the whole data layer.
const OpSnapshot = Operation("snapshot")

// Timestamp mirrors Google's [seconds, nanos] pair. It marshals as an RFC 3339
// string so the frontend never has to reassemble the halves, and as null when
// the server omitted it.
type Timestamp struct {
	Seconds int64 `json:"-"`
	Nanos   int64 `json:"-"`
}

// IsZero reports whether the server sent no timestamp at all.
func (t Timestamp) IsZero() bool { return t.Seconds == 0 && t.Nanos == 0 }

// Time converts the pair to a time.Time in local time.
func (t Timestamp) Time() time.Time { return time.Unix(t.Seconds, t.Nanos) }

// MarshalJSON renders the timestamp as RFC 3339, or null when absent.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time().Format(time.RFC3339))
}

// Due is a task's due date plus optional time of day. The public Tasks API only
// carries the date, which is one of the reasons this client exists. In the read
// payload it is field 18 of the task's detail message — a message holding one
// timestamp — and Time stays empty for an all-day due.
type Due struct {
	Date string `json:"date"`
	// Time is empty for an all-day due, which is how the web app tells the two
	// apart too.
	Time string `json:"time,omitempty"`
}

// SlotBag keeps positional slots that have not been mapped to a meaning yet,
// verbatim. Google documents none of this, so the bag is how a layout change
// gets noticed: a new key shows up here long before a field silently vanishes
// from the parsed structs.
type SlotBag map[int]json.RawMessage

// TaskList is one list in the sidebar.
type TaskList struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Kind is the server's own label for the list, "user" for lists the
	// account created.
	Kind string `json:"kind,omitempty"`
	// Index is the position in the sidebar, taken from the server's list
	// order rather than from the order the entries happened to arrive in.
	Index int `json:"index"`
	// TaskIDs is the server-declared display order of the list's incomplete
	// tasks. It is authoritative: the sort key in the task payload is not
	// monotonic, so this array is the only reliable ordering.
	TaskIDs []string `json:"-"`
	// Incomplete is the count the sidebar badge shows.
	Incomplete int `json:"incomplete"`
	// CompletedTotal is the real number of completed tasks, which is larger
	// than the number actually returned: the snapshot pages completed tasks.
	CompletedTotal int       `json:"completedTotal"`
	Created        Timestamp `json:"created"`
	Updated        Timestamp `json:"updated"`
	MetaExtra      SlotBag   `json:"metaExtra,omitempty"`
	Extra          SlotBag   `json:"extra,omitempty"`
}

// Task is one task.
type Task struct {
	ID          string    `json:"id"`
	ListID      string    `json:"listId"`
	Title       string    `json:"title"`
	Notes       string    `json:"notes,omitempty"`
	Completed   bool      `json:"completed"`
	CompletedAt Timestamp `json:"completedAt"`
	Created     Timestamp `json:"created"`
	Updated     Timestamp `json:"updated"`
	Starred     bool      `json:"starred"`
	StarredAt   Timestamp `json:"starredAt"`
	// Parent is derived from the parent's child list rather than read from a
	// slot, so it stays correct whatever position Google uses for it.
	Parent   string   `json:"parent,omitempty"`
	Children []string `json:"children,omitempty"`
	Due      *Due     `json:"due,omitempty"`
	// DetailExtra and Extra hold the still-unmapped slots of the detail and
	// task arrays respectively.
	DetailExtra SlotBag `json:"detailExtra,omitempty"`
	Extra       SlotBag `json:"extra,omitempty"`
}

// Snapshot is the whole dataset as the web app holds it in memory.
type Snapshot struct {
	ServerTime Timestamp `json:"serverTime"`
	// Account is the numeric Google account id the payload belongs to.
	Account string `json:"account,omitempty"`
	// ActiveListID is the list the web app had open when the payload was
	// produced; it is not a client-side selection.
	ActiveListID string     `json:"activeListId,omitempty"`
	ListOrder    []string   `json:"listOrder,omitempty"`
	Lists        []TaskList `json:"lists"`
	Tasks        []Task     `json:"tasks"`
}

// Slot indexes pinned down by diffing live payloads against the rendered web
// UI. Anything outside these sets is preserved in a SlotBag.
var (
	knownTaskSlots   = map[int]bool{0: true, 1: true, 2: true, 3: true, 8: true, 10: true, 12: true}
	knownDetailSlots = map[int]bool{0: true, 1: true, 2: true, 4: true, 13: true, 14: true, 17: true}
	knownListSlots   = map[int]bool{0: true, 1: true, 2: true, 5: true, 7: true, 9: true}
	knownMetaSlots   = map[int]bool{0: true, 2: true}
)

// ParseSnapshot decodes the payload of the snapshot RPC.
//
// The payload is a positional array rather than a keyed object, so it is walked
// with json.RawMessage at every level. That keeps the bytes of the slots we do
// not understand intact for the SlotBags instead of round-tripping them through
// float64.
func ParseSnapshot(payload json.RawMessage) (*Snapshot, error) {
	root, err := decodeArray(payload)
	if err != nil {
		return nil, fmt.Errorf("tasks: 快照不是 JSON 数组: %w", err)
	}
	if len(root) < 2 {
		return nil, errors.New("tasks: 快照缺少数据段")
	}

	snapshot := &Snapshot{}
	meta, err := decodeArray(root[0])
	if err != nil {
		return nil, fmt.Errorf("tasks: 快照头部不是 JSON 数组: %w", err)
	}
	snapshot.ServerTime = rawTimestamp(at(meta, 0))
	if account, err := decodeArray(at(meta, 6)); err == nil {
		if ids, err := decodeArray(at(account, 1)); err == nil {
			snapshot.Account = rawString(at(ids, 0))
		}
	}

	entries, err := decodeArray(root[1])
	if err != nil {
		return nil, fmt.Errorf("tasks: 快照数据段不是 JSON 数组: %w", err)
	}

	// Each entry is [a, b, c, d] with exactly one slot populated: c is a task,
	// d is a list and b carries the sidebar order. The first entry is a
	// one-element wrapper and falls through all three cases.
	var lists []TaskList
	for _, entry := range entries {
		fields, err := decodeArray(entry)
		if err != nil {
			return nil, fmt.Errorf("tasks: 快照条目不是 JSON 数组: %w", err)
		}
		switch {
		case len(fields) > 3 && !isNull(fields[3]):
			list, err := parseTaskList(fields[3])
			if err != nil {
				return nil, err
			}
			lists = append(lists, list)
		case len(fields) > 2 && !isNull(fields[2]):
			task, err := parseTask(fields[2])
			if err != nil {
				return nil, err
			}
			snapshot.Tasks = append(snapshot.Tasks, task)
		case len(fields) > 1 && !isNull(fields[1]):
			active, order, err := parseListOrder(fields[1])
			if err != nil {
				return nil, err
			}
			snapshot.ActiveListID = active
			snapshot.ListOrder = order
		}
	}

	snapshot.Lists = orderLists(lists, snapshot.ListOrder)
	snapshot.linkParents()
	snapshot.orderTasks()
	return snapshot, nil
}

// IncompleteTasks returns the tasks of one list in display order, skipping
// completed ones. An empty listID returns every incomplete task.
func (s *Snapshot) IncompleteTasks(listID string) []Task {
	out := make([]Task, 0, len(s.Tasks))
	for _, task := range s.Tasks {
		if task.Completed {
			continue
		}
		if listID != "" && task.ListID != listID {
			continue
		}
		out = append(out, task)
	}
	return out
}

// StarredTasks returns the incomplete starred tasks across every list, which is
// what the 已加星标 view shows.
func (s *Snapshot) StarredTasks() []Task {
	out := make([]Task, 0, len(s.Tasks))
	for _, task := range s.Tasks {
		if task.Starred && !task.Completed {
			out = append(out, task)
		}
	}
	return out
}

// List finds a list by id.
func (s *Snapshot) List(id string) (TaskList, bool) {
	for _, list := range s.Lists {
		if list.ID == id {
			return list, true
		}
	}
	return TaskList{}, false
}

func parseTask(inner json.RawMessage) (Task, error) {
	fields, err := decodeArray(inner)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: 任务不是 JSON 数组: %w", err)
	}
	task := Task{
		ID:       rawString(at(fields, 0)),
		Updated:  rawTimestamp(at(fields, 2)),
		ListID:   rawString(at(fields, 3)),
		Children: parseIDList(at(fields, 8)),
		Created:  rawTimestamp(at(fields, 10)),
	}
	if task.ID == "" {
		return Task{}, errors.New("tasks: 任务缺少 id")
	}
	if detail, err := decodeArray(at(fields, 1)); err == nil && len(detail) > 0 {
		task.Completed = rawBool(at(detail, 0))
		task.Title = rawString(at(detail, 1))
		task.Notes = rawString(at(detail, 2))
		task.CompletedAt = rawTimestamp(at(detail, 4))
		// Starred is the field the public Tasks API does not expose at all.
		// It is a flag at 13 plus the time it was set at 14.
		task.Starred = rawBool(at(detail, 13))
		task.StarredAt = rawTimestamp(at(detail, 14))
		task.Due = rawDue(at(detail, 17))
		task.DetailExtra = keepUnknown(detail, knownDetailSlots)
	}
	task.Extra = keepUnknown(fields, knownTaskSlots)
	return task, nil
}

func parseTaskList(raw json.RawMessage) (TaskList, error) {
	fields, err := decodeArray(raw)
	if err != nil {
		return TaskList{}, fmt.Errorf("tasks: 清单不是 JSON 数组: %w", err)
	}
	list := TaskList{
		ID:      rawString(at(fields, 0)),
		Created: rawTimestamp(at(fields, 5)),
		Updated: rawTimestamp(at(fields, 7)),
	}
	if list.ID == "" {
		return TaskList{}, errors.New("tasks: 清单缺少 id")
	}
	if meta, err := decodeArray(at(fields, 1)); err == nil {
		list.Title = rawString(at(meta, 0))
		list.Kind = rawString(at(meta, 2))
		list.MetaExtra = keepUnknown(meta, knownMetaSlots)
	}
	// The declared order arrives one level deeper than the task child lists:
	// [[[id], [id], ...]].
	if wrapper, err := decodeArray(at(fields, 2)); err == nil {
		list.TaskIDs = parseIDList(at(wrapper, 0))
	}
	list.Incomplete = len(list.TaskIDs)
	// Slot 9 is a bare count, and the server leaves it out entirely for lists
	// that have never had a task completed. It is the number the 已完成 header
	// shows, which is larger than the completed tasks actually returned.
	list.CompletedTotal = int(rawInt(at(fields, 9)))
	list.Extra = keepUnknown(fields, knownListSlots)
	return list, nil
}

// parseListOrder reads the sidebar entry: slot 0 is the list the web app had
// open and slot 2 is the comma separated order of every list.
func parseListOrder(raw json.RawMessage) (active string, order []string, err error) {
	fields, err := decodeArray(raw)
	if err != nil {
		return "", nil, fmt.Errorf("tasks: 清单排序条目不是 JSON 数组: %w", err)
	}
	active = rawString(at(fields, 0))
	for _, id := range strings.Split(rawString(at(fields, 2)), ",") {
		if id = strings.TrimSpace(id); id != "" {
			order = append(order, id)
		}
	}
	return active, order, nil
}

// orderLists puts the lists in sidebar order. Lists the server did not mention
// in the order entry sort after the ones it did, by title, so a newly created
// list still shows up.
func orderLists(lists []TaskList, order []string) []TaskList {
	rank := make(map[string]int, len(order))
	for index, id := range order {
		rank[id] = index
	}
	sort.SliceStable(lists, func(i, j int) bool {
		left, leftKnown := rank[lists[i].ID]
		right, rightKnown := rank[lists[j].ID]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && left != right {
			return left < right
		}
		return lists[i].Title < lists[j].Title
	})
	for index := range lists {
		lists[index].Index = index
	}
	return lists
}

// linkParents fills each task's Parent from its parent's child list.
func (s *Snapshot) linkParents() {
	indexByID := make(map[string]int, len(s.Tasks))
	for index := range s.Tasks {
		indexByID[s.Tasks[index].ID] = index
	}
	for index := range s.Tasks {
		parent := &s.Tasks[index]
		for _, child := range parent.Children {
			if at, ok := indexByID[child]; ok && s.Tasks[at].Parent == "" {
				s.Tasks[at].Parent = parent.ID
			}
		}
	}
}

// orderTasks lays the tasks out the way the web app renders them: per list in
// sidebar order, the server-declared incomplete order first, then the completed
// ones newest first. The snapshot pages completed tasks, so the completed run
// is usually shorter than the list's CompletedTotal.
func (s *Snapshot) orderTasks() {
	indexByID := make(map[string]int, len(s.Tasks))
	for index := range s.Tasks {
		indexByID[s.Tasks[index].ID] = index
	}

	taken := make(map[string]bool, len(s.Tasks))
	ordered := make([]Task, 0, len(s.Tasks))
	for _, list := range s.Lists {
		for _, id := range list.TaskIDs {
			index, ok := indexByID[id]
			if !ok || taken[id] {
				continue
			}
			ordered = append(ordered, s.Tasks[index])
			taken[id] = true
		}
		var rest []Task
		for index := range s.Tasks {
			task := s.Tasks[index]
			if taken[task.ID] || task.ListID != list.ID {
				continue
			}
			rest = append(rest, task)
			taken[task.ID] = true
		}
		sortTasks(rest)
		ordered = append(ordered, rest...)
	}
	// Tasks whose list did not come back keep their payload order.
	for index := range s.Tasks {
		if !taken[s.Tasks[index].ID] {
			ordered = append(ordered, s.Tasks[index])
		}
	}
	s.Tasks = ordered
}

func sortTasks(tasks []Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		left, right := tasks[i], tasks[j]
		if left.Completed != right.Completed {
			return !left.Completed
		}
		if left.Completed && left.CompletedAt.Seconds != right.CompletedAt.Seconds {
			return left.CompletedAt.Seconds > right.CompletedAt.Seconds
		}
		return left.ID < right.ID
	})
}

// keepUnknown collects every populated slot that is not in known, so the
// payload's remaining structure stays inspectable.
func keepUnknown(fields []json.RawMessage, known map[int]bool) SlotBag {
	var bag SlotBag
	for index, raw := range fields {
		if known[index] || isBlank(raw) {
			continue
		}
		if bag == nil {
			bag = SlotBag{}
		}
		bag[index] = raw
	}
	return bag
}

// parseIDList reads Google's [[id], [id], ...] shape, and tolerates the flat
// [id, id, ...] form too.
func parseIDList(raw json.RawMessage) []string {
	rows, err := decodeArray(raw)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if inner, err := decodeArray(row); err == nil && len(inner) > 0 {
			if id := rawString(inner[0]); id != "" {
				ids = append(ids, id)
			}
			continue
		}
		if id := rawString(row); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// decodeArray splits a JSON array into its raw element bytes. A null or absent
// slot decodes to no elements and no error, matching how Google omits whole
// subtrees instead of sending nulls for them.
func decodeArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if isBlank(raw) {
		return nil, nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func at(fields []json.RawMessage, index int) json.RawMessage {
	if index < 0 || index >= len(fields) {
		return nil
	}
	return fields[index]
}

func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// isBlank reports whether a slot carries no information at all. Empty arrays
// count as blank so the SlotBags stay readable.
func isBlank(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return true
	}
	switch string(trimmed) {
	case "null", "[]", `""`:
		return true
	}
	return false
}

func rawString(raw json.RawMessage) string {
	if isNull(raw) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return text
}

func rawBool(raw json.RawMessage) bool {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return value
}

func rawInt(raw json.RawMessage) int64 {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0
	}
	if value, err := number.Int64(); err == nil {
		return value
	}
	value, err := number.Float64()
	if err != nil {
		return 0
	}
	return int64(value)
}

// rawDue reads the due slot of a task's detail message: a one field message
// wrapping the [秒, 纳秒] pair, which is why it sits a level deeper than every
// other timestamp in the payload.
//
// Google stores an all-day due as UTC midnight, and that is also how the two
// forms are told apart on the way back out: UTC midnight renders as a bare date,
// anything else as the local date plus the local time of day.
func rawDue(raw json.RawMessage) *Due {
	outer, err := decodeArray(raw)
	if err != nil || len(outer) == 0 {
		return nil
	}
	stamp := rawTimestamp(at(outer, 0))
	if stamp.IsZero() {
		return nil
	}
	value := stamp.Time().UTC()
	if value.Hour() == 0 && value.Minute() == 0 && value.Second() == 0 && value.Nanosecond() == 0 {
		return &Due{Date: value.Format("2006-01-02")}
	}
	local := stamp.Time()
	return &Due{Date: local.Format("2006-01-02"), Time: local.Format("15:04")}
}

func rawTimestamp(raw json.RawMessage) Timestamp {
	parts, err := decodeArray(raw)
	if err != nil {
		return Timestamp{}
	}
	return Timestamp{Seconds: rawInt(at(parts, 0)), Nanos: rawInt(at(parts, 1))}
}
