package memory

import (
	"flag"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/raintank/met"
	"github.com/raintank/metrictank/idx"
	"github.com/raintank/worldping-api/pkg/log"
	"github.com/rakyll/globalconf"
	"gopkg.in/raintank/schema.v1"
)

var (
	idxOk             met.Count
	idxFail           met.Count
	idxAddDuration    met.Timer
	idxGetDuration    met.Timer
	idxListDuration   met.Timer
	idxFindDuration   met.Timer
	idxDeleteDuration met.Timer

	Enabled bool
)

func ConfigSetup() {
	memoryIdx := flag.NewFlagSet("memory-idx", flag.ExitOnError)
	memoryIdx.BoolVar(&Enabled, "enabled", true, "")
	globalconf.Register("memory-idx", memoryIdx)
}

type Tree struct {
	Items map[string]*Node
}

type Node struct {
	Path     string
	Children []string
	Leaf     bool
}

func (n *Node) String() string {
	t := "branch"
	if n.Leaf {
		t = "leaf"
	}
	return fmt.Sprintf("%s - %s", t, n.Path)
}

// Implements the the "MetricIndex" interface
type MemoryIdx struct {
	sync.RWMutex
	DefById map[string]*schema.MetricDefinition
	Tree    map[int]*Tree
}

func New() *MemoryIdx {
	return &MemoryIdx{
		DefById: make(map[string]*schema.MetricDefinition),
		Tree:    make(map[int]*Tree),
	}
}

func (m *MemoryIdx) Init(stats met.Backend) error {
	idxOk = stats.NewCount("idx.memory.ok")
	idxFail = stats.NewCount("idx.memory.fail")
	idxAddDuration = stats.NewTimer("idx.memory.add_duration", 0)
	idxGetDuration = stats.NewTimer("idx.memory.get_duration", 0)
	idxListDuration = stats.NewTimer("idx.memory.list_duration", 0)
	idxFindDuration = stats.NewTimer("idx.memory.find_duration", 0)
	idxDeleteDuration = stats.NewTimer("idx.memory.delete_duration", 0)
	return nil
}

func (m *MemoryIdx) Stop() {
	return
}

func (m *MemoryIdx) Add(data *schema.MetricData) {
	pre := time.Now()
	m.Lock()
	defer m.Unlock()
	existing, ok := m.DefById[data.Id]
	if ok {
		log.Debug("IDX-M metricDef with id %s already in index.", data.Id)
		existing.LastUpdate = data.Time
		idxOk.Inc(1)
		idxAddDuration.Value(time.Since(pre))
		return
	}

	def := schema.MetricDefinitionFromMetricData(data)
	m.add(def)
	idxAddDuration.Value(time.Since(pre))
}

// Used to rebuild the index from an existing set of metricDefinitions.
func (m *MemoryIdx) Load(defs []schema.MetricDefinition) {
	m.Lock()
	var pre time.Time
	for i, _ := range defs {
		def := defs[i]
		pre = time.Now()
		if _, ok := m.DefById[def.Id]; ok {
			continue
		}
		m.add(&def)
		idxAddDuration.Value(time.Since(pre))
	}
	m.Unlock()
}

func (m *MemoryIdx) AddDef(def *schema.MetricDefinition) {
	pre := time.Now()
	m.Lock()
	defer m.Unlock()
	if existing, ok := m.DefById[def.Id]; ok {
		log.Debug("IDX-M metricDef with id %s already in index.", def.Id)
		existing.LastUpdate = def.LastUpdate
		idxOk.Inc(1)
		idxAddDuration.Value(time.Since(pre))
		return
	}
	m.add(def)
	idxAddDuration.Value(time.Since(pre))
}

func (m *MemoryIdx) add(def *schema.MetricDefinition) {
	m.DefById[def.Id] = def
	path := def.Name
	//first check to see if a tree has been created for this OrgId
	tree, ok := m.Tree[def.OrgId]
	if !ok {
		log.Debug("IDX-M first metricDef seen for orgId %d", def.OrgId)
		root := &Node{
			Path:     "",
			Children: make([]string, 0),
			Leaf:     false,
		}
		m.Tree[def.OrgId] = &Tree{
			Items: map[string]*Node{"": root},
		}
		tree = m.Tree[def.OrgId]
	} else {
		// now see if there is alread a leaf node. This happens
		// when there are multiple metricDefs for the same path due
		// to different tags or interval
		if node, ok := tree.Items[path]; ok {
			if !node.Leaf {
				//bad data. A path cant be both a leaf and a branch.
				log.Info("IDX-M Bad data, a path can not be both a leaf and a branch. %d - %s", def.OrgId, path)
				idxFail.Inc(1)
				return
			}
			log.Debug("IDX-M existing index entry for %s. Adding %s as child", path, def.Id)
			node.Children = append(node.Children, def.Id)
			idxOk.Inc(1)
			return
		}
	}
	// now walk backwards through the node path to find the first branch which exists that
	// this path extends.
	nodes := strings.Split(path, ".")
	startPos := 0
	var startNode *Node
	if len(nodes) > 1 {
		for i := len(nodes) - 1; i > 0; i-- {
			branch := strings.Join(nodes[0:i], ".")
			if n, ok := tree.Items[branch]; ok {
				if n.Leaf {
					log.Info("IDX-M Branches cant be added to a leaf node. %d - %s", def.OrgId, path)
					idxFail.Inc(1)
					return
				}
				log.Debug("IDX-M Found branch %s which metricDef %s is a decendent of", branch, path)
				startNode = n
				startPos = i
				break
			}
		}
	}
	if startPos == 0 && startNode == nil {
		// need to add to the root node.
		log.Debug("IDX-M no existing branches found for %s.  Adding to the root node.", path)
		startNode = tree.Items[""]
	}

	log.Debug("IDX-M adding %s as child of %s", nodes[startPos], startNode.Path)
	startNode.Children = append(startNode.Children, nodes[startPos])
	startPos++

	// Add missing branch nodes
	for i := startPos; i < len(nodes); i++ {
		branch := strings.Join(nodes[0:i], ".")
		log.Debug("IDX-M creating branch %s with child %s", branch, nodes[i])
		tree.Items[branch] = &Node{
			Path:     branch,
			Leaf:     false,
			Children: []string{nodes[i]},
		}
	}

	// Add leaf node
	log.Debug("IDX-M creating leaf %s", path)
	tree.Items[path] = &Node{
		Leaf:     true,
		Path:     path,
		Children: []string{def.Id},
	}
	idxOk.Inc(1)
	return
}

func (m *MemoryIdx) Get(id string) (schema.MetricDefinition, error) {
	pre := time.Now()
	m.RLock()
	defer m.RUnlock()
	def, ok := m.DefById[id]
	if ok {
		idxGetDuration.Value(time.Since(pre))
		return *def, nil
	}
	idxGetDuration.Value(time.Since(pre))
	return schema.MetricDefinition{}, idx.DefNotFound
}

func (m *MemoryIdx) Find(orgId int, pattern string) ([]idx.Node, error) {
	pre := time.Now()
	m.RLock()
	defer m.RUnlock()
	matchedNodes, err := m.find(orgId, pattern)
	if err != nil {
		return nil, err
	}
	publicNodes, err := m.find(-1, pattern)
	if err != nil {
		return nil, err
	}
	matchedNodes = append(matchedNodes, publicNodes...)
	log.Debug("IDX-M %d nodes matching pattern %s found", len(matchedNodes), pattern)
	results := make([]idx.Node, 0)
	seen := make(map[string]struct{})
	// if there are public (orgId -1) and private leaf nodes with the same series
	// path, then the public metricDefs will be excluded.
	for _, n := range matchedNodes {
		if _, ok := seen[n.Path]; !ok {
			idxNode := idx.Node{
				Path: n.Path,
				Leaf: n.Leaf,
			}
			if n.Leaf {
				idxNode.Defs = make([]schema.MetricDefinition, len(n.Children))
				for i, id := range n.Children {
					idxNode.Defs[i] = *m.DefById[id]
				}
			}
			results = append(results, idxNode)
			seen[n.Path] = struct{}{}
		} else {
			log.Debug("IDX-M path %s already seen", n.Path)
		}
	}
	log.Debug("IDX-M %d nodes has %d unique paths.", len(matchedNodes), len(results))
	idxFindDuration.Value(time.Since(pre))
	return results, nil
}

func (m *MemoryIdx) find(orgId int, pattern string) ([]*Node, error) {
	tree, ok := m.Tree[orgId]
	if !ok {
		log.Debug("IDX-M orgId %d has no metrics indexed.", orgId)
		return []*Node{}, nil
	}

	nodes := strings.Split(pattern, ".")
	pos := len(nodes) - 1
	for i := 0; i < len(nodes); i++ {
		if strings.ContainsAny(nodes[i], "*{}[]?") {
			log.Debug("IDX-M found first pattern sequence at node %s pos %d", nodes[i], i)
			pos = i - 1
			break
		}
	}
	results := make([]*Node, 0)
	var startNode *Node
	if pos == -1 {
		//we need to start at the root.
		log.Debug("IDX-M starting search at the root node")
		startNode = tree.Items[""]
	} else {
		branch := strings.Join(nodes[0:pos+1], ".")
		log.Debug("IDX-M starting search at branch %s", branch)
		startNode, ok = tree.Items[branch]
		if !ok {
			log.Debug("IDX-M branch %s does not exist in the index for orgId %d", branch, orgId)
			return results, nil
		}
	}

	if pos == len(nodes)-1 {
		// startNode is the leaf we want.
		log.Debug("IDX-M pattern %s was a specific branch/leaf name.", pattern)
		results = append(results, startNode)
		return results, nil
	}

	children := []*Node{startNode}
	for pos < len(nodes) {
		pos++
		if pos == len(nodes) {
			log.Debug("IDX-M reached pattern length at node pos %d. %d nodes matched", pos, len(children))
			for _, c := range children {
				results = append(results, c)
			}
			continue
		}
		grandChildren := make([]*Node, 0)
		for _, c := range children {
			if c.Leaf {
				log.Debug("IDX-M leaf node %s found but we havent reached the end of the pattern %s", c.Path, pattern)
				// expecting a branch
				continue
			}
			log.Debug("IDX-M searching %d children of %s that match %s", len(c.Children), c.Path, nodes[pos])
			matches, err := match(nodes[pos], c.Children)
			if err != nil {
				return results, err
			}
			for _, m := range matches {
				newBranch := c.Path + "." + m
				if c.Path == "" {
					newBranch = m
				}
				grandChildren = append(grandChildren, tree.Items[newBranch])
			}
		}
		children = grandChildren
		if len(children) == 0 {
			log.Debug("IDX-M pattern does not match any series.")
			break
		}
	}

	return results, nil
}

func match(pattern string, candidates []string) ([]string, error) {
	var patterns []string
	if strings.ContainsAny(pattern, "{}") {
		patterns = expandQueries(pattern)
	} else {
		patterns = []string{pattern}
	}

	results := make([]string, 0)
	for _, p := range patterns {
		if strings.ContainsAny(p, "*[]?") {
			p = strings.Replace(p, "*", ".*", -1)
			p = strings.Replace(p, "?", ".?", -1)
			p = "^" + p + "$"
			r, err := regexp.Compile(p)
			if err != nil {
				log.Debug("IDX-M regexp failed to compile. %s - %s", p, err)
				return nil, err
			}
			for _, c := range candidates {
				if r.MatchString(c) {
					log.Debug("IDX-M %s matches %s", c, p)
					results = append(results, c)
				}
			}
		} else {
			for _, c := range candidates {
				if c == p {
					log.Debug("IDX-M %s is exact match", c)
					results = append(results, c)
				}
			}
		}
	}
	return results, nil
}

func (m *MemoryIdx) List(orgId int) []schema.MetricDefinition {
	pre := time.Now()
	m.RLock()
	defer m.RUnlock()
	orgs := []int{-1, orgId}
	if orgId == -1 {
		log.Info("IDX-M returing all metricDefs for all orgs")
		orgs = make([]int, len(m.Tree))
		i := 0
		for org := range m.Tree {
			orgs[i] = org
			i++
		}
	}
	defs := make([]schema.MetricDefinition, 0)
	for _, org := range orgs {
		tree, ok := m.Tree[org]
		if !ok {
			continue
		}
		for _, n := range tree.Items {
			if !n.Leaf {
				continue
			}
			for _, id := range n.Children {
				defs = append(defs, *m.DefById[id])
			}
		}
	}
	idxListDuration.Value(time.Since(pre))

	return defs
}

func (m *MemoryIdx) Delete(orgId int, pattern string) error {
	pre := time.Now()
	m.Lock()
	defer m.Unlock()
	found, err := m.find(orgId, pattern)
	if err != nil {
		return err
	}
	for _, f := range found {
		m.delete(orgId, f)
	}
	idxDeleteDuration.Value(time.Since(pre))
	return nil
}

func (m *MemoryIdx) DeleteWithReport(orgId int, pattern string) ([]string, error) {
	pre := time.Now()
	m.Lock()
	defer m.Unlock()
	found, err := m.find(orgId, pattern)
	if err != nil {
		return nil, err
	}
	deletedIds := make([]string, 0)
	for _, f := range found {
		deleted, err := m.delete(orgId, f)
		if err != nil {
			return nil, err
		}
		deletedIds = append(deletedIds, deleted...)
	}
	idxDeleteDuration.Value(time.Since(pre))
	return deletedIds, nil
}

func (m *MemoryIdx) delete(orgId int, n *Node) ([]string, error) {
	deletedIds := make([]string, len(n.Children))
	if !n.Leaf {
		log.Debug("IDX-M deleting branch %s", n.Path)
		// walk up the tree to find all leaf nodes and delete them.
		children, err := m.find(orgId, n.Path+".*")
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			log.Debug("IDX-M deleting child %s from branch %s", child.Path, n.Path)
			deleted, err := m.delete(orgId, child)
			if err != nil {
				return deletedIds, err
			}
			deletedIds = append(deletedIds, deleted...)
		}
	}
	// delete the metricDefs
	for i, id := range n.Children {
		delete(m.DefById, id)
		deletedIds[i] = id
	}
	tree := m.Tree[orgId]
	// delete the leaf.
	delete(tree.Items, n.Path)

	// delete from the branches
	nodes := strings.Split(n.Path, ".")
	for i := len(nodes) - 1; i >= 0; i-- {
		branch := strings.Join(nodes[0:i], ".")
		log.Debug("IDX-M removing %s from branch %s", nodes[i], branch)
		bNode, ok := tree.Items[branch]
		if !ok {
			log.Error(3, "IDX-M Branch %s missing. Index is corrupt.", branch)
			continue
		}
		if len(bNode.Children) > 1 {
			newChildren := make([]string, 0)
			for _, child := range bNode.Children {
				if child != nodes[i] {
					newChildren = append(newChildren, child)
				} else {
					log.Debug("IDX-M %s removed from children list of branch %s", child, bNode.Path)
				}
			}
			bNode.Children = newChildren
			log.Debug("IDX-M branch %s has other children. Leaving it in place", bNode.Path)
			// no need to delete any parents as they are needed by this node and its
			// remaining children
			break
		}

		if bNode.Children[0] != nodes[i] {
			log.Error(3, "IDX-M %s not in children list for branch %s. Index is corrupt", nodes[i], branch)
			break
		}
		log.Debug("IDX-M branch %s has no children, deleting it.", branch)
		delete(tree.Items, branch)
	}

	return deletedIds, nil
}

// filepath.Match doesn't support {} because that's not posix, it's a bashism
// the easiest way of implementing this extra feature is just expanding single queries
// that contain these queries into multiple queries, who will be checked separately
// and whose results will be ORed.
func expandQueries(query string) []string {
	queries := []string{query}

	// as long as we find a { followed by a }, split it up into subqueries, and process
	// all queries again
	// we only stop once there are no more queries that still have {..} in them
	keepLooking := true
	for keepLooking {
		expanded := make([]string, 0)
		keepLooking = false
		for _, query := range queries {
			lbrace := strings.Index(query, "{")
			rbrace := -1
			if lbrace > -1 {
				rbrace = strings.Index(query[lbrace:], "}")
				if rbrace > -1 {
					rbrace += lbrace
				}
			}

			if lbrace > -1 && rbrace > -1 {
				keepLooking = true
				expansion := query[lbrace+1 : rbrace]
				options := strings.Split(expansion, ",")
				for _, option := range options {
					expanded = append(expanded, query[:lbrace]+option+query[rbrace+1:])
				}
			} else {
				expanded = append(expanded, query)
			}
		}
		queries = expanded
	}
	return queries
}
