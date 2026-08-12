// Token budget allocation (File 06 §6.6.1). The window is split into a reply
// reservation (a hard floor, never borrowed from) and a waterfall across input
// groups with strict priorities: system > project > conversation > files >
// user. The Context Engine computes this; the Prompt Compiler enforces it.

package context

// allocate splits a token window into the §6.6.1 budget. Reserve is 15% with a
// 1024 floor; system is capped at 12% (4096 max); project at 8% (2048 max);
// conversation gets 45%; files 17%; RAG 8%; user the remainder. RAG (retrieved
// code chunks, File 11 §11.6) takes a slice off files — semantic recall is
// cheaper to recompute than re-reading disk, so RAG trims before Files when
// over budget.
func allocate(window int) Budget {
	reserve := window * 15 / 100
	if reserve < 1024 {
		reserve = 1024
	}
	avail := window - reserve
	if avail < 0 {
		avail = 0
	}
	sys := avail * 12 / 100
	if sys > 4096 {
		sys = 4096
	}
	proj := avail * 8 / 100
	if proj > 2048 {
		proj = 2048
	}
	conv := avail * 45 / 100
	files := avail * 17 / 100
	rag := avail * 8 / 100
	user := avail - sys - proj - conv - files - rag
	if user < 0 {
		user = 0
	}
	return Budget{
		Window: window, Reserve: reserve,
		System: sys, Project: proj, Conversation: conv, Files: files, RAG: rag, User: user,
	}
}
