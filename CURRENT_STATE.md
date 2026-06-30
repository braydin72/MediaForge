CURRENT STATE NOTE

As of this session, actively working on:
1. Windows browse path fix for mapped network drives (M:\) and UNC paths — 
   sent to Claude Code, status unknown, check git log
2. Nil pointer panic in internal/browse/browse.go during WarmCountCache on 
   network shares — sent to Claude Code, status unknown, check git log
3. Build number injection (internal/version/version.go) + TMDB confidence 
   scoring rewrite — sent to Claude Code, status unknown, check git log
4. Windows tray app (cmd/tray/main.go) using getlantern/systray — sent to 
   Claude Code, status unknown, check git log
5. Inno Setup installer script written (installer/mediaforge.iss), NOT yet 
   tested locally, NOT yet wired into CI

NOT yet started:
- SmartShrink quality cascade (Excellent -> Good -> Acceptable fallback)
- CRF search range expansion (28 down to 16)
- VMAF sample count configurable

Key files for current work:
- internal/browse/browse.go (WarmCountCache, nil pointer issue)
- internal/api/handler.go (path validation for browse endpoint)
- internal/intake/tmdb.go (movie confidence scoring)
- internal/intake/tvdb.go (TV confidence scoring - DONE, 4-component formula)
- internal/version/version.go (build number - may not exist yet)
- cmd/tray/main.go (tray app - may not exist yet, build-tag windows)
- installer/mediaforge.iss (Inno Setup script - written, untested)
- .github/workflows/release.yml (needs windows-installer job added)

ALWAYS run "git log --oneline -10" at the start of a new session to see 
what actually landed vs what was requested, before assuming anything is 
done or not done.
