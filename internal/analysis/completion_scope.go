package analysis

import (
	"fmt"
	"strings"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang/catalog"
	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/funcdocs"
	"github.com/paulourio/bqlang/internal/syntax"
)

func completeFromPhantomNode(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTStatementNode,
	cursorID *googlesql.ASTIdentifier,
	_ string,
	pipeCols []pipeColumnInfo,
) []CompletionItem {
	parent := astParent(cursorID)
	if pe, ok := parent.(*googlesql.ASTPathExpression); ok && !isNilNode(pe) {
		peParent := astParent(pe)
		segs := pathSegmentsBeforeCursor(pe)
		if _, isTablePath := peParent.(*googlesql.ASTTablePathExpression); isTablePath {
			return completeTablePaths(snap, astStmt, segs)
		}

		if len(segs) > 0 {
			return completeQualifiedPath(snap, astStmt, cursorID, segs)
		}
	}

	if dotID, ok := parent.(*googlesql.ASTDotIdentifier); ok && !isNilNode(dotID) {
		ex, _ := dotID.Expr()
		if pe, isPE := ex.(*googlesql.ASTPathExpression); isPE && !isNilNode(pe) {
			segs, _ := pe.ToIdentifierVector()
			if len(segs) > 0 {
				return completeQualifiedPath(snap, astStmt, cursorID, segs)
			}
		}
	}

	return completeGeneralScope(snap, astStmt, cursorID, pipeCols)
}

func pathSegmentsBeforeCursor(pe *googlesql.ASTPathExpression) []string {
	parts, _ := pe.ToIdentifierVector()
	var segs []string
	for _, p := range parts {
		if p == phantomCursorIdent {
			break
		}
		segs = append(segs, p)
	}

	return segs
}

func completeTablePaths(snap *DocumentSnapshot, astStmt googlesql.ASTNode, segs []string) []CompletionItem {
	var items []CompletionItem
	if len(segs) == 0 {
		for _, cte := range collectAllCTENames(astStmt) {
			items = append(items, CompletionItem{
				Label:    cte,
				Kind:     CompletionKindStruct,
				Detail:   "CTE",
				SortText: "0_" + cte,
			})
		}

		for _, tt := range snap.TempTables {
			items = append(items, CompletionItem{
				Label:    tt.Ref.Table,
				Kind:     CompletionKindClass,
				Detail:   "TEMP TABLE",
				SortText: "0_" + tt.Ref.Table,
			})
		}
	}

	if snap.Store == nil {
		return items
	}

	items = append(items, storeTablePathCompletions(snap, segs)...)

	return items
}

func storeTablePathCompletions(snap *DocumentSnapshot, segs []string) []CompletionItem {
	var items []CompletionItem
	defProj := ""
	defDS := ""
	if snap.Options != nil {
		defProj = snap.Options.DefaultProject
		defDS = snap.Options.DefaultDataset
	}

	switch len(segs) {
	case 0:
		for _, ds := range snap.Store.ListDatasets(defProj) {
			items = append(items, CompletionItem{
				Label:    ds,
				Kind:     CompletionKindModule,
				Detail:   "Dataset",
				SortText: "1_" + ds,
			})
		}

		for _, tbl := range snap.Store.ListCachedTables() {
			items = append(items, tableMetaCompletionItem(tbl, tbl.Ref.Table, "1_"))
			if tbl.Ref.Dataset != "" {
				items = append(items, tableMetaCompletionItem(tbl, tbl.Ref.Dataset+"."+tbl.Ref.Table, "2_"))
			}
		}
	case 1:
		for _, ds := range snap.Store.ListDatasets(segs[0]) {
			items = append(items, CompletionItem{
				Label:    ds,
				Kind:     CompletionKindModule,
				Detail:   "Dataset",
				SortText: "0_" + ds,
			})
		}

		for _, tbl := range snap.Store.ListTables(defProj, segs[0]) {
			items = append(items, tableMetaCompletionItem(tbl, tbl.Ref.Table, "0_"))
		}
	case 2:
		for _, tbl := range snap.Store.ListTables(segs[0], segs[1]) {
			items = append(items, tableMetaCompletionItem(tbl, tbl.Ref.Table, "0_"))
		}
	default:
	}

	_ = defDS

	return items
}

func tableMetaCompletionItem(meta *catalog.TableMeta, label, sortPrefix string) CompletionItem {
	var doc *MarkupContent
	if meta.Description != "" {
		doc = &MarkupContent{Kind: "markdown", Value: meta.Description}
	}

	kindLabel := string(meta.Type)
	if kindLabel == "" {
		kindLabel = "TABLE"
	}

	return CompletionItem{
		Label:         label,
		LabelDetails:  &CompletionItemLabelDetails{Description: kindLabel},
		Kind:          CompletionKindClass,
		Detail:        meta.Ref.FullName(),
		Documentation: doc,
		SortText:      sortPrefix + label,
	}
}

func completeQualifiedPath(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	cursorID *googlesql.ASTIdentifier,
	segs []string,
) []CompletionItem {
	if len(segs) == 1 {
		if fnItems := completeFunctionNamespace(segs[0]); len(fnItems) > 0 {
			return fnItems
		}
	}

	if items := resolveAliasOrSubqueryCompletion(snap, astStmt, cursorID, segs); len(items) > 0 {
		return items
	}

	return resolveStructColumnFallback(snap, astStmt, cursorID, segs)
}

func completeFunctionNamespace(ns string) []CompletionItem {
	upperNS := strings.ToUpper(ns)
	prefix := upperNS + "."

	var items []CompletionItem
	seen := make(map[string]struct{})

	for _, doc := range funcdocs.ByNamespace(upperNS) {
		suffix := strings.TrimPrefix(doc.Name, prefix)
		seen[suffix] = struct{}{}
		items = append(items, funcDocCompletionItem(suffix, doc, "0_"))
	}

	for _, fn := range formatter.BigQueryBuiltinFunctions() {
		if strings.HasPrefix(strings.ToUpper(fn), prefix) {
			suffix := fn[len(prefix):]
			if _, exists := seen[strings.ToUpper(suffix)]; exists {
				continue
			}

			items = append(items, CompletionItem{
				Label:    suffix,
				Kind:     CompletionKindFunction,
				Detail:   fn,
				SortText: "0_" + suffix,
			})
		}
	}

	return items
}

func resolveAliasOrSubqueryCompletion(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	cursorID googlesql.ASTNode,
	segs []string,
) []CompletionItem {
	targetAlias := segs[0]

	// 1. Check enclosing SELECT FROM clauses (innermost first), then statement-wide fallback.
	if meta := findTableMetaForAlias(snap, astStmt, cursorID, targetAlias); meta != nil {
		return completeColumnsOfTableMeta(meta, segs[1:])
	}

	// 2. Check CTEs matching alias or referenced via alias.
	if cteCols := findTypedCTEOrSubqueryColsForAlias(snap, astStmt, targetAlias); len(cteCols) > 0 && len(segs) == 1 {
		return typedColsToCompletionItems(cteCols, "CTE / subquery column")
	}

	return nil
}

func resolveStructColumnFallback(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	cursorID googlesql.ASTNode,
	segs []string,
) []CompletionItem {
	for _, meta := range collectInScopeTableMetas(snap, astStmt, cursorID) {
		if field, ok := meta.FindField(segs...); ok && len(field.Fields) > 0 {
			return structFieldsToCompletionItems(meta, segs, field.Fields)
		}
	}

	return nil
}

func findTableMetaForAlias(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	cursorID googlesql.ASTNode,
	alias string,
) *catalog.TableMeta {
	for cur := cursorID; !isNilNode(cur); cur = astParent(cur) {
		if sel, ok := cur.(*googlesql.ASTSelect); ok && !isNilNode(sel) {
			fc, _ := sel.FromClause()
			if meta := matchAliasInTableExpr(snap, astStmt, fc, alias); meta != nil {
				return meta
			}
		}
	}

	// Statement-wide ASTTablePathExpression fallback (e.g., unclosed subquery parens).
	var found *catalog.TableMeta
	walkASTNodes(astStmt, func(n googlesql.ASTNode) bool {
		if found != nil {
			return false
		}

		if tp, ok := n.(*googlesql.ASTTablePathExpression); ok && !isNilNode(tp) {
			if tablePathMatchesAlias(tp, alias) {
				if meta := tableMetaFromTablePath(snap, tp); meta != nil {
					found = meta
					return false
				}
			}
		}

		return true
	})

	return found
}

func matchAliasInTableExpr(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	root googlesql.ASTNode,
	alias string,
) *catalog.TableMeta {
	if isNilNode(root) {
		return nil
	}

	var found *catalog.TableMeta
	walkASTNodes(root, func(n googlesql.ASTNode) bool {
		if found != nil {
			return false
		}

		switch node := n.(type) {
		case *googlesql.ASTTablePathExpression:
			if tablePathMatchesAlias(node, alias) {
				if meta := tableMetaFromTablePath(snap, node); meta != nil {
					found = meta
					return false
				}
			}
		case *googlesql.ASTTableSubquery:
			if al, _ := node.Alias(); !isNilNode(al) {
				if name, _ := al.GetAsString(); strings.EqualFold(name, alias) {
					return false
				}
			}
		default:
		}

		return true
	})

	_ = astStmt

	return found
}

func tablePathMatchesAlias(tp *googlesql.ASTTablePathExpression, alias string) bool {
	if al, _ := tp.Alias(); !isNilNode(al) {
		if name, _ := al.GetAsString(); strings.EqualFold(name, alias) {
			return true
		}
	}

	if pe, _ := tp.PathExpr(); !isNilNode(pe) {
		parts, _ := pe.ToIdentifierVector()
		norm := normalizeASTTableParts(parts)
		if len(norm) > 0 && strings.EqualFold(norm[len(norm)-1], alias) {
			return true
		}
	}

	return false
}

func storeDefaultProjectDataset(snap *DocumentSnapshot) (project, dataset string) {
	if snap.Options != nil {
		project = snap.Options.DefaultProject
		dataset = snap.Options.DefaultDataset
	}

	if snap.Store != nil {
		if project == "" {
			project = snap.Store.DefaultProject()
		}
		if dataset == "" {
			dataset = snap.Store.DefaultDataset()
		}
	}

	return project, dataset
}

func tableMetaFromTablePath(snap *DocumentSnapshot, tp *googlesql.ASTTablePathExpression) *catalog.TableMeta {
	pe, err := tp.PathExpr()
	if err != nil || isNilNode(pe) {
		return nil
	}

	parts, _ := pe.ToIdentifierVector()
	if len(parts) == 0 {
		return nil
	}

	norm := normalizeASTTableParts(parts)
	if len(norm) == 0 {
		return nil
	}

	for _, tt := range snap.TempTables {
		if strings.EqualFold(tt.Ref.Table, norm[len(norm)-1]) && tt.Meta != nil {
			return tt.Meta
		}
	}

	if snap.Store != nil {
		defProj, defDS := storeDefaultProjectDataset(snap)
		ref := tableRefFromPath(norm, defProj, defDS)
		if meta, ok := snap.Store.GetTable(ref); ok {
			return meta
		}
	}

	return nil
}

func findCTEOrSubqueryColsForAlias(astStmt googlesql.ASTNode, alias string) []string {
	var cols []string
	walkASTNodes(astStmt, func(n googlesql.ASTNode) bool {
		if len(cols) > 0 {
			return false
		}

		cols = matchNodeColsForAlias(astStmt, n, alias)

		return len(cols) == 0
	})

	return cols
}

func matchNodeColsForAlias(astStmt, n googlesql.ASTNode, alias string) []string {
	switch node := n.(type) {
	case *googlesql.ASTTablePathExpression:
		if !tablePathMatchesAlias(node, alias) {
			return nil
		}

		if pe, _ := node.PathExpr(); !isNilNode(pe) {
			if parts, _ := pe.ToIdentifierVector(); len(parts) == 1 {
				return findCTEColumnsInAST(astStmt, parts[0])
			}
		}
	case *googlesql.ASTTableSubquery:
		if al, _ := node.Alias(); !isNilNode(al) {
			if name, _ := al.GetAsString(); strings.EqualFold(name, alias) {
				q, _ := node.Subquery()
				return extractSelectAliasesFromQuery(q)
			}
		}
	default:
	}

	return nil
}

func completeColumnsOfTableMeta(meta *catalog.TableMeta, subPath []string) []CompletionItem {
	if len(subPath) > 0 {
		field, ok := meta.FindField(subPath...)
		if !ok {
			return nil
		}

		return structFieldsToCompletionItems(meta, subPath, field.Fields)
	}

	items := make([]CompletionItem, 0, len(meta.Schema)+2)
	for i := range meta.Schema {
		col := &meta.Schema[i]
		items = append(items, columnCompletionItem(meta, col.Name, col))
	}

	if meta.IsIngestionTimePartitioned() {
		items = append(items,
			CompletionItem{
				Label:        "_PARTITIONTIME",
				LabelDetails: &CompletionItemLabelDetails{Description: "TIMESTAMP [PSEUDO] [PARTITION]"},
				Kind:         CompletionKindField,
				Detail:       "TIMESTAMP [PSEUDO] [PARTITION]",
				SortText:     "00__PARTITIONTIME",
			},
			CompletionItem{
				Label:        "_PARTITIONDATE",
				LabelDetails: &CompletionItemLabelDetails{Description: "DATE [PSEUDO] [PARTITION]"},
				Kind:         CompletionKindField,
				Detail:       "DATE [PSEUDO] [PARTITION]",
				SortText:     "00__PARTITIONDATE",
			},
		)
	}

	if strings.HasSuffix(meta.Ref.Table, "*") {
		items = append(items, CompletionItem{
			Label:        "_TABLE_SUFFIX",
			LabelDetails: &CompletionItemLabelDetails{Description: "STRING [PSEUDO]"},
			Kind:         CompletionKindField,
			Detail:       "STRING [PSEUDO]",
			SortText:     "01__TABLE_SUFFIX",
		})
	}

	return items
}

func structFieldsToCompletionItems(meta *catalog.TableMeta, prefix []string, fields []catalog.ColumnMeta) []CompletionItem {
	items := make([]CompletionItem, 0, len(fields))
	for i := range fields {
		f := &fields[i]
		fullPath := strings.Join(append(append([]string(nil), prefix...), f.Name), ".")
		items = append(items, columnCompletionItem(meta, fullPath, f))
	}

	return items
}

func columnCompletionItem(meta *catalog.TableMeta, fullPath string, col *catalog.ColumnMeta) CompletionItem {
	sortPrefix := "2_"
	var badges []string
	if meta.IsPartitionColumn(fullPath) {
		sortPrefix = "0_"
		badges = append(badges, "[PARTITION]")
	}

	if rank, ok := meta.ClusterRank(fullPath); ok {
		if sortPrefix == "2_" {
			sortPrefix = fmt.Sprintf("1_%02d_", rank)
		}
		badges = append(badges, fmt.Sprintf("[CLUSTER #%d]", rank))
	}

	sqlType := col.SQLType()
	detail := sqlType
	inlineDesc := compactInlineType(col)
	if len(badges) > 0 {
		badgeStr := strings.Join(badges, " ")
		detail += " " + badgeStr
		inlineDesc += " " + badgeStr
	}

	docMD := formatColumnCompletionDoc(fullPath, sqlType, col)

	return CompletionItem{
		Label:         col.Name,
		LabelDetails:  &CompletionItemLabelDetails{Description: inlineDesc},
		Kind:          CompletionKindField,
		Detail:        detail,
		Documentation: &MarkupContent{Kind: "markdown", Value: docMD},
		SortText:      sortPrefix + col.Name,
	}
}

func compactInlineType(col *catalog.ColumnMeta) string {
	sqlType := col.SQLType()
	if len(sqlType) <= 42 {
		return sqlType
	}

	if col.EffectiveMode() == catalog.ModeRepeated {
		if strings.HasPrefix(sqlType, "ARRAY<STRUCT<") {
			return "ARRAY<STRUCT<...>>"
		}
	}

	if strings.HasPrefix(sqlType, "STRUCT<") {
		return "STRUCT<...>"
	}

	return sqlType
}

func formatColumnCompletionDoc(fullPath, sqlType string, col *catalog.ColumnMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**`%s`** : `%s` (`%s`)", fullPath, sqlType, col.EffectiveMode())
	if col.Description != "" {
		fmt.Fprintf(&b, "\n\n%s", col.Description)
	}

	return b.String()
}

type typedColInfo struct {
	name string
	typ  string
}

func findTypedCTEOrSubqueryColsForAlias(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	alias string,
) []typedColInfo {
	names := findCTEOrSubqueryColsForAlias(astStmt, alias)
	if len(names) == 0 {
		return nil
	}

	cteName := resolveCTENameForAlias(astStmt, alias)
	typeByLower := collectResolvedCTETypes(snap, astStmt, cteName)
	out := make([]typedColInfo, 0, len(names))
	for _, nm := range names {
		out = append(out, typedColInfo{
			name: nm,
			typ:  typeByLower[strings.ToLower(nm)],
		})
	}

	return out
}

func resolveCTENameForAlias(astStmt googlesql.ASTNode, alias string) string {
	if isNilNode(astStmt) {
		return alias
	}

	resolved := alias
	walkASTNodes(astStmt, func(n googlesql.ASTNode) bool {
		tp, ok := n.(*googlesql.ASTTablePathExpression)
		if !ok || isNilNode(tp) || !tablePathMatchesAlias(tp, alias) {
			return true
		}

		if pe, _ := tp.PathExpr(); !isNilNode(pe) {
			if parts, _ := pe.ToIdentifierVector(); len(parts) == 1 && parts[0] != "" {
				resolved = parts[0]
				return false
			}
		}

		return true
	})

	return resolved
}

func collectResolvedCTETypes(snap *DocumentSnapshot, astStmt googlesql.ASTNode, alias string) map[string]string {
	types := make(map[string]string)

	for _, st := range snap.Statements {
		if st != nil && !isNilNode(st.ResolvedStmt) {
			collectCTETypesFromResolvedStmt(st.ResolvedStmt, alias, types)
		}
	}

	if len(types) > 0 || isNilNode(astStmt) {
		return types
	}

	inferCTETypesFromASTTables(snap, astStmt, alias, types)

	return types
}

func collectCTETypesFromResolvedStmt(rs googlesql.ResolvedStatementNode, alias string, out map[string]string) {
	walkResolvedNodes(rs, func(n googlesql.ResolvedNode) bool {
		we, ok := n.(*googlesql.ResolvedWithEntry)
		if !ok || isNilNode(we) {
			return true
		}

		wName, _ := we.WithQueryName()
		if !strings.EqualFold(wName, alias) {
			return true
		}

		sub, _ := we.WithSubquery()
		for _, rc := range scanColumnList(sub) {
			if isNilNode(rc) {
				continue
			}

			cn, _ := rc.Name()
			ct, _ := rc.Type()
			if cn != "" {
				out[strings.ToLower(cn)] = formatGoogleSQLType(ct)
			}
		}

		return false
	})
}

func inferCTETypesFromASTTables(snap *DocumentSnapshot, astStmt googlesql.ASTNode, cteName string, out map[string]string) {
	walkASTNodes(astStmt, func(n googlesql.ASTNode) bool {
		aq, ok := n.(*googlesql.ASTAliasedQuery)
		if !ok || isNilNode(aq) {
			return true
		}

		al, _ := aq.Alias()
		if isNilNode(al) {
			return true
		}

		name, _ := al.GetAsString()
		if !strings.EqualFold(name, cteName) {
			return true
		}

		q, _ := aq.Query()
		inferQuerySelectColumnTypes(snap, q, out)

		return false
	})
}

func inferQuerySelectColumnTypes(snap *DocumentSnapshot, q *googlesql.ASTQuery, out map[string]string) {
	if isNilNode(q) {
		return
	}

	expr, _ := q.QueryExpr()
	sel, ok := expr.(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return
	}

	metas := collectFromClauseTableMetas(snap, sel)
	if len(metas) == 0 {
		return
	}

	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return
	}

	for i := range astNumChildren(sl) {
		sc, isSC := astChild(sl, i).(*googlesql.ASTSelectColumn)
		if !isSC || isNilNode(sc) {
			continue
		}

		if outName, sqlType, found := inferSelectColumnType(sc, metas); found {
			out[strings.ToLower(outName)] = sqlType
		}
	}
}

func collectFromClauseTableMetas(snap *DocumentSnapshot, sel *googlesql.ASTSelect) []*catalog.TableMeta {
	fc, _ := sel.FromClause()
	var metas []*catalog.TableMeta
	walkASTNodes(fc, func(n googlesql.ASTNode) bool {
		if tp, isTP := n.(*googlesql.ASTTablePathExpression); isTP && !isNilNode(tp) {
			if m := tableMetaFromTablePath(snap, tp); m != nil {
				metas = append(metas, m)
			}
		}

		return true
	})

	return metas
}

func inferSelectColumnType(sc *googlesql.ASTSelectColumn, metas []*catalog.TableMeta) (outName, sqlType string, ok bool) {
	outName = selectColumnName(sc)
	if outName == "" {
		return "", "", false
	}

	ex, _ := sc.Expression()
	pe, isPE := ex.(*googlesql.ASTPathExpression)
	if !isPE || isNilNode(pe) {
		return "", "", false
	}

	last, _ := pe.LastName()
	if isNilNode(last) {
		return "", "", false
	}

	srcCol, _ := last.GetAsString()
	for _, m := range metas {
		if cm, found := m.FindColumn(srcCol); found {
			return outName, cm.SQLType(), true
		}
	}

	return "", "", false
}

func typedColsToCompletionItems(cols []typedColInfo, fallbackDetail string) []CompletionItem {
	items := make([]CompletionItem, 0, len(cols))
	for _, c := range cols {
		detail := fallbackDetail
		var ld *CompletionItemLabelDetails
		if c.typ != "" {
			detail = c.typ
			ld = &CompletionItemLabelDetails{Description: c.typ}
		}

		items = append(items, CompletionItem{
			Label:        c.name,
			LabelDetails: ld,
			Kind:         CompletionKindField,
			Detail:       detail,
			SortText:     "0_" + c.name,
		})
	}

	return items
}

func completeGeneralScope(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	cursorID *googlesql.ASTIdentifier,
	pipeCols []pipeColumnInfo,
) []CompletionItem {
	var items []CompletionItem

	if fnUnits := completeFunctionPositionalUnits(cursorID); len(fnUnits) > 0 {
		items = append(items, fnUnits...)
	}

	for _, c := range pipeCols {
		var ld *CompletionItemLabelDetails
		if c.typ != "" {
			ld = &CompletionItemLabelDetails{Description: c.typ}
		}

		items = append(items, CompletionItem{
			Label:        c.name,
			LabelDetails: ld,
			Kind:         CompletionKindField,
			Detail:       c.typ,
			SortText:     "0_" + c.name,
		})
	}

	if len(pipeCols) == 0 {
		for _, meta := range collectInScopeTableMetas(snap, astStmt, cursorID) {
			items = append(items, completeColumnsOfTableMeta(meta, nil)...)
		}

		for _, cte := range collectAllCTENames(astStmt) {
			typeByLower := collectResolvedCTETypes(snap, astStmt, cte)
			for _, c := range findCTEColumnsInAST(astStmt, cte) {
				typ := typeByLower[strings.ToLower(c)]
				detail := "CTE " + cte
				var ld *CompletionItemLabelDetails
				if typ != "" {
					detail = typ + " (CTE " + cte + ")"
					ld = &CompletionItemLabelDetails{Description: typ}
				}

				items = append(items, CompletionItem{
					Label:        c,
					LabelDetails: ld,
					Kind:         CompletionKindField,
					Detail:       detail,
					SortText:     "1_" + c,
				})
			}
		}
	}

	items = append(items, completeVariablesAndFunctions(snap)...)
	items = append(items, clauseKeywordCompletions()...)

	return items
}

func completeFunctionPositionalUnits(cursorID *googlesql.ASTIdentifier) []CompletionItem {
	fnCall := findEnclosingFuncCall(cursorID)
	if isNilNode(fnCall) {
		return nil
	}

	fnPath, _ := fnCall.Function()
	if isNilNode(fnPath) {
		return nil
	}

	parts, _ := fnPath.ToIdentifierVector()
	fnName := strings.ToUpper(strings.Join(parts, "."))
	unitArg := positionalUnitArgIndex(fnName)
	if unitArg < 0 && !strings.HasPrefix(fnName, "DATE_") && !strings.HasPrefix(fnName, "DATETIME_") &&
		!strings.HasPrefix(fnName, "TIMESTAMP_") && !strings.HasPrefix(fnName, "TIME_") {
		return nil
	}

	units := []string{
		"MICROSECOND", "MILLISECOND", "SECOND", "MINUTE", "HOUR",
		"DAY", "WEEK", "ISOWEEK", "MONTH", "QUARTER", "YEAR", "ISOYEAR",
	}
	if fnName == "NORMALIZE" || fnName == "NORMALIZE_AND_CASEFOLD" {
		units = []string{"NFC", "NFD", "NFKC", "NFKD"}
	}

	items := make([]CompletionItem, 0, len(units))
	for i, u := range units {
		items = append(items, CompletionItem{
			Label:    u,
			Kind:     CompletionKindEnumMember,
			Detail:   "Date/time or normalization unit",
			SortText: fmt.Sprintf("0_%02d_%s", i, u),
		})
	}

	return items
}

func findEnclosingFuncCall(n googlesql.ASTNode) *googlesql.ASTFunctionCall {
	for cur := astParent(n); !isNilNode(cur); cur = astParent(cur) {
		if fc, ok := cur.(*googlesql.ASTFunctionCall); ok {
			return fc
		}
	}

	return nil
}

func collectInScopeTableMetas(
	snap *DocumentSnapshot,
	astStmt googlesql.ASTNode,
	cursorID googlesql.ASTNode,
) []*catalog.TableMeta {
	seen := make(map[string]struct{})
	var metas []*catalog.TableMeta

	addTP := func(tp *googlesql.ASTTablePathExpression) {
		meta := tableMetaFromTablePath(snap, tp)
		if meta == nil {
			return
		}
		key := strings.ToLower(meta.Ref.FullName())
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		metas = append(metas, meta)
	}

	for cur := cursorID; !isNilNode(cur); cur = astParent(cur) {
		if sel, ok := cur.(*googlesql.ASTSelect); ok && !isNilNode(sel) {
			fc, _ := sel.FromClause()
			walkASTNodes(fc, func(n googlesql.ASTNode) bool {
				if tp, ok := n.(*googlesql.ASTTablePathExpression); ok && !isNilNode(tp) {
					addTP(tp)
				}
				return true
			})
		}
	}

	if len(metas) == 0 && !isNilNode(astStmt) {
		walkASTNodes(astStmt, func(n googlesql.ASTNode) bool {
			if tp, ok := n.(*googlesql.ASTTablePathExpression); ok && !isNilNode(tp) {
				addTP(tp)
			}
			return true
		})
	}

	return metas
}

func collectAllCTENames(astStmt googlesql.ASTNode) []string {
	if isNilNode(astStmt) {
		return nil
	}

	var names []string
	walkASTNodes(astStmt, func(n googlesql.ASTNode) bool {
		if aq, ok := n.(*googlesql.ASTAliasedQuery); ok && !isNilNode(aq) {
			if al, _ := aq.Alias(); !isNilNode(al) {
				if name, _ := al.GetAsString(); name != "" {
					names = append(names, name)
				}
			}
		}

		return true
	})

	return names
}

func extractSelectAliasesFromQuery(q *googlesql.ASTQuery) []string {
	if isNilNode(q) {
		return nil
	}

	expr, _ := q.QueryExpr()
	sel, ok := expr.(*googlesql.ASTSelect)
	if !ok || isNilNode(sel) {
		return nil
	}

	sl, _ := sel.SelectList()
	if isNilNode(sl) {
		return nil
	}

	var cols []string
	n := astNumChildren(sl)
	for i := range n {
		if sc, isSC := astChild(sl, i).(*googlesql.ASTSelectColumn); isSC && !isNilNode(sc) {
			if name := selectColumnName(sc); name != "" {
				cols = append(cols, name)
			}
		}
	}

	return cols
}

func selectColumnName(sc *googlesql.ASTSelectColumn) string {
	if al, _ := sc.Alias(); !isNilNode(al) {
		if name, _ := al.GetAsString(); name != "" {
			return name
		}
	}

	ex, _ := sc.Expression()
	if pe, ok := ex.(*googlesql.ASTPathExpression); ok && !isNilNode(pe) {
		if last, _ := pe.LastName(); !isNilNode(last) {
			if name, _ := last.GetAsString(); name != "" {
				return name
			}
		}
	}

	return ""
}

func completeQualifiedFromTokens(snap *DocumentSnapshot, stmtSQL, prefix string) ([]CompletionItem, bool) {
	toks := syntax.Scan(prefix)
	i := len(toks) - 1
	if i >= 0 && toks[i].Kind == syntax.TokenIdent {
		i--
	}

	if i < 1 || toks[i].Kind != syntax.TokenPunct || toks[i].Text(prefix) != "." {
		return nil, false
	}

	var segs []string
	for i >= 1 && toks[i].Kind == syntax.TokenPunct && toks[i].Text(prefix) == "." {
		i--
		if i >= 0 && (toks[i].Kind == syntax.TokenIdent || toks[i].Kind == syntax.TokenBacktickIdent) {
			segs = append(segs, strings.Trim(toks[i].Text(prefix), "`"))
			i--
		} else {
			break
		}
	}

	if len(segs) == 0 {
		return nil, false
	}

	reverseStrings(segs)
	if len(segs) == 1 {
		if fnItems := completeFunctionNamespace(segs[0]); len(fnItems) > 0 {
			return fnItems, true
		}
	}

	alias := segs[0]
	tableParts := findTablePartsForAliasInTokens(prefix, toks, alias)
	if len(tableParts) == 0 && len(stmtSQL) > len(prefix) {
		allToks := syntax.Scan(stmtSQL)
		tableParts = findTablePartsForAliasInTokens(stmtSQL, allToks, alias)
	}

	if len(tableParts) > 0 {
		if meta := lookupTableMetaByParts(snap, tableParts); meta != nil {
			return completeColumnsOfTableMeta(meta, segs[1:]), true
		}
	}

	return nil, false
}

func lookupTableMetaByParts(snap *DocumentSnapshot, tableParts []string) *catalog.TableMeta {
	norm := normalizeASTTableParts(tableParts)
	if len(norm) == 0 {
		return nil
	}

	for _, tt := range snap.TempTables {
		if strings.EqualFold(tt.Ref.Table, norm[len(norm)-1]) && tt.Meta != nil {
			return tt.Meta
		}
	}

	if snap.Store != nil {
		defProj, defDS := storeDefaultProjectDataset(snap)
		ref := tableRefFromPath(norm, defProj, defDS)
		if meta, ok := snap.Store.GetTable(ref); ok {
			return meta
		}
	}

	return nil
}

func reverseStrings(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func findTablePartsForAliasInTokens(src string, toks []syntax.Token, alias string) []string {
	var nonTrivia []syntax.Token
	for _, t := range toks {
		if !t.IsTrivia() {
			nonTrivia = append(nonTrivia, t)
		}
	}

	for i := range nonTrivia {
		kw := strings.ToUpper(nonTrivia[i].Text(src))
		if kw != "FROM" && kw != "JOIN" {
			continue
		}

		parts, nextIdx := readDottedIdentFromTokens(src, nonTrivia, i+1)
		if len(parts) == 0 {
			continue
		}

		tblAlias := parts[len(parts)-1]
		if nextIdx < len(nonTrivia) && strings.EqualFold(nonTrivia[nextIdx].Text(src), "AS") && nextIdx+1 < len(nonTrivia) {
			tblAlias = strings.Trim(nonTrivia[nextIdx+1].Text(src), "`")
		} else if nextIdx < len(nonTrivia) && nonTrivia[nextIdx].Kind == syntax.TokenIdent {
			cand := strings.ToUpper(nonTrivia[nextIdx].Text(src))
			if _, isKW := sqlKeywordSet[cand]; !isKW {
				tblAlias = nonTrivia[nextIdx].Text(src)
			}
		}

		if strings.EqualFold(tblAlias, alias) {
			return parts
		}
	}

	return nil
}

func readDottedIdentFromTokens(src string, nonTrivia []syntax.Token, start int) ([]string, int) {
	if start >= len(nonTrivia) {
		return nil, start
	}

	tok := nonTrivia[start]
	if tok.Kind == syntax.TokenBacktickIdent {
		inner := strings.Trim(tok.Text(src), "`")
		return strings.Split(inner, "."), start + 1
	}

	if tok.Kind != syntax.TokenIdent {
		return nil, start
	}

	parts := []string{tok.Text(src)}
	idx := start + 1
	for idx+1 < len(nonTrivia) && nonTrivia[idx].Kind == syntax.TokenPunct && nonTrivia[idx].Text(src) == "." {
		if nonTrivia[idx+1].Kind == syntax.TokenIdent || nonTrivia[idx+1].Kind == syntax.TokenBacktickIdent {
			parts = append(parts, strings.Trim(nonTrivia[idx+1].Text(src), "`"))
			idx += 2
		} else {
			break
		}
	}

	return parts, idx
}

func isAfterTableKeyword(prefix string) bool {
	toks := syntax.Scan(prefix)
	i := len(toks) - 1
	if i >= 0 && toks[i].Kind == syntax.TokenIdent {
		i--
	}

	for i >= 0 && toks[i].IsTrivia() {
		i--
	}

	if i < 0 {
		return false
	}

	kw := strings.ToUpper(toks[i].Text(prefix))

	return kw == "FROM" || kw == "JOIN" || kw == "INTO" || kw == "TABLE" || kw == "UPDATE"
}
