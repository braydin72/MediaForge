package setup

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/braydin72/mediaforge/internal/config"
)

// IsFirstRun returns true when the setup wizard must run.
// cfgFileExisted should be true if the config file was present on disk before
// config.Load was called (Load creates the file with defaults when absent).
func IsFirstRun(cfgFileExisted bool, cfg *config.Config) bool {
	if !cfgFileExisted {
		return true
	}
	if cfg.Intake.WatchFolder == "" {
		return true
	}
	if cfg.Intake.Library.Movies == "" {
		return true
	}
	if cfg.Intake.Library.TVShows == "" {
		return true
	}
	if cfg.APIs.TMDBKey == "" {
		return true
	}
	if cfg.APIs.TVDBKey == "" {
		return true
	}
	return false
}

// WizardHandler wraps an existing http.Handler, intercepting all non-API
// requests until the first-run wizard is submitted and config is saved.
type WizardHandler struct {
	main     http.Handler
	cfg      *config.Config
	cfgPath  string
	done     chan struct{}
	complete atomic.Bool
}

// NewWizardHandler returns a WizardHandler that wraps main.
func NewWizardHandler(main http.Handler, cfgPath string, cfg *config.Config) *WizardHandler {
	return &WizardHandler{
		main:    main,
		cfg:     cfg,
		cfgPath: cfgPath,
		done:    make(chan struct{}),
	}
}

// Done returns a channel that is closed when setup completes.
func (w *WizardHandler) Done() <-chan struct{} {
	return w.done
}

func (w *WizardHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	// The setup submission endpoint is always handled here.
	if r.URL.Path == "/api/setup" {
		if r.Method == http.MethodPost {
			w.handleSetupSubmit(rw, r)
		} else {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// After wizard completion, all traffic goes to the main handler.
	if w.complete.Load() {
		w.main.ServeHTTP(rw, r)
		return
	}

	// During wizard mode: let /api/* pass through so SSE / job streams work.
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.main.ServeHTTP(rw, r)
		return
	}

	// Serve the wizard page at /setup.
	if r.URL.Path == "/setup" {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(http.StatusOK)
		rw.Write([]byte(wizardHTML)) //nolint:errcheck
		return
	}

	// Redirect everything else (including /) to /setup.
	http.Redirect(rw, r, "/setup", http.StatusTemporaryRedirect)
}

type setupRequest struct {
	WatchFolder   string `json:"watch_folder"`
	StagingFolder string `json:"staging_folder"`
	MoviesLib     string `json:"movies_library"`
	TVShowsLib    string `json:"tvshows_library"`
	TMDBKey       string `json:"tmdb_key"`
	TVDBKey       string `json:"tvdb_key"`
	OMDbKey       string `json:"omdb_key"`
	LLMBackend    string `json:"llm_backend"`
	LLMAPIKey     string `json:"llm_api_key"`
	LLMModel      string `json:"llm_model"`
	OllamaHost    string `json:"ollama_host"`
}

func (w *WizardHandler) handleSetupSubmit(rw http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(rw, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.WatchFolder == "" || req.MoviesLib == "" || req.TVShowsLib == "" ||
		req.TMDBKey == "" || req.TVDBKey == "" {
		http.Error(rw, "missing required fields", http.StatusBadRequest)
		return
	}

	w.cfg.Intake.Enabled = true
	w.cfg.Intake.WatchFolder = filepath.FromSlash(req.WatchFolder)
	if req.StagingFolder != "" {
		w.cfg.Intake.StagingFolder = filepath.FromSlash(req.StagingFolder)
	}
	w.cfg.Intake.Library.Movies = filepath.FromSlash(req.MoviesLib)
	w.cfg.Intake.Library.TVShows = filepath.FromSlash(req.TVShowsLib)
	w.cfg.APIs.TMDBKey = req.TMDBKey
	w.cfg.APIs.TVDBKey = req.TVDBKey
	w.cfg.APIs.OMDbKey = req.OMDbKey
	w.cfg.LLM.Backend = req.LLMBackend
	w.cfg.LLM.APIKey = req.LLMAPIKey
	w.cfg.LLM.Model = req.LLMModel
	if req.OllamaHost != "" {
		w.cfg.LLM.OllamaHost = req.OllamaHost
	}

	if err := w.cfg.Save(w.cfgPath); err != nil {
		http.Error(rw, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if w.complete.CompareAndSwap(false, true) {
		close(w.done)
	}

	rw.Header().Set("Content-Type", "application/json")
	rw.Write([]byte(`{"ok":true}`)) //nolint:errcheck
}

const wizardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>MediaForge - First Run Setup</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',system-ui,sans-serif;background:#0f1117;color:#e2e8f0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:20px}
.wizard{background:#1a1d2e;border:1px solid #2d3250;border-radius:12px;width:100%;max-width:560px;padding:40px}
.logo{text-align:center;margin-bottom:32px}
.logo h1{font-size:28px;font-weight:700;color:#6366f1;letter-spacing:-0.5px}
.logo p{color:#94a3b8;font-size:14px;margin-top:4px}
.bars{display:flex;gap:8px;margin-bottom:8px}
.bar{flex:1;height:4px;border-radius:2px;background:#2d3250;transition:background 0.3s}
.bar.active{background:#6366f1}
.bar.done{background:#4ade80}
.step-label{text-align:center;font-size:13px;color:#94a3b8;margin-bottom:28px;line-height:1.5}
.step-label strong{color:#e2e8f0;font-size:15px;display:block;margin-bottom:4px}
.field{margin-bottom:20px}
.field label{display:block;font-size:13px;font-weight:500;color:#cbd5e1;margin-bottom:6px}
.req{color:#f87171;margin-left:2px}
.opt{color:#64748b;font-weight:400;font-size:12px;margin-left:4px}
.field input,.field select{width:100%;background:#0f1117;border:1px solid #2d3250;border-radius:8px;padding:10px 14px;font-size:14px;color:#e2e8f0;outline:none;transition:border-color 0.2s}
.field input:focus,.field select:focus{border-color:#6366f1}
.field input.error{border-color:#f87171}
.field select option{background:#1a1d2e}
.hint{font-size:12px;color:#64748b;margin-top:4px}
.errmsg{font-size:12px;color:#f87171;margin-top:4px;display:none}
.actions{display:flex;gap:12px;margin-top:32px}
.btn{flex:1;padding:12px 20px;border-radius:8px;font-size:14px;font-weight:600;border:none;cursor:pointer;transition:opacity 0.2s}
.btn:disabled{opacity:0.4;cursor:not-allowed}
.btn-primary{background:#6366f1;color:#fff}
.btn-primary:hover:not(:disabled){background:#5253cc}
.btn-secondary{background:#2d3250;color:#e2e8f0}
.btn-secondary:hover:not(:disabled){background:#374170}
.err-banner{display:none;background:#450a0a;border:1px solid #7f1d1d;border-radius:8px;padding:12px 16px;font-size:13px;color:#fca5a5;margin-bottom:20px}
#success{display:none;text-align:center;padding:20px 0}
#success .icon{font-size:48px;color:#4ade80;margin-bottom:16px}
#success h2{font-size:22px;color:#4ade80;margin-bottom:8px}
#success p{color:#94a3b8;font-size:14px}
</style>
</head>
<body>
<div class="wizard">
  <div class="logo">
    <h1>MediaForge</h1>
    <p>First-Run Setup</p>
  </div>

  <div class="bars">
    <div class="bar active" id="b1"></div>
    <div class="bar" id="b2"></div>
    <div class="bar" id="b3"></div>
  </div>
  <div class="step-label" id="lbl">
    <strong>Step 1 of 3: Folders</strong>
    Configure where MediaForge watches for new files and stores your library.
  </div>

  <div class="err-banner" id="banner"></div>

  <div id="s1">
    <div class="field">
      <label>Watch Folder <span class="req">*</span></label>
      <input type="text" id="watch_folder" placeholder="C:\Incoming  or  /incoming">
      <div class="hint">MediaForge monitors this folder for new video files.</div>
      <div class="errmsg" id="e-watch_folder">Required</div>
    </div>
    <div class="field">
      <label>Staging Folder <span class="opt">(optional)</span></label>
      <input type="text" id="staging_folder" placeholder="C:\Staging  or  /staging">
      <div class="hint">AVC files are moved here before transcoding. Use a fast local disk. Can be configured later.</div>
    </div>
    <div class="field">
      <label>Movies Library <span class="req">*</span></label>
      <input type="text" id="movies_library" placeholder="\\server\share\Movies  or  /media/Movies">
      <div class="hint">Destination for identified movie files. UNC paths are supported.</div>
      <div class="errmsg" id="e-movies_library">Required</div>
    </div>
    <div class="field">
      <label>TV Shows Library <span class="req">*</span></label>
      <input type="text" id="tvshows_library" placeholder="\\server\share\TV Shows  or  /media/TV Shows">
      <div class="hint">Destination for identified TV episode files.</div>
      <div class="errmsg" id="e-tvshows_library">Required</div>
    </div>
  </div>

  <div id="s2" style="display:none">
    <div class="field">
      <label>TMDB API Key <span class="req">*</span></label>
      <input type="password" id="tmdb_key" placeholder="Your TMDB v3 API key">
      <div class="hint">Required for movie lookups and TV fallback. Free at themoviedb.org.</div>
      <div class="errmsg" id="e-tmdb_key">Required</div>
    </div>
    <div class="field">
      <label>TVDB API Key <span class="req">*</span></label>
      <input type="password" id="tvdb_key" placeholder="Your TVDB v4 API key">
      <div class="hint">Required for TV show lookups (primary source). Free account at thetvdb.com.</div>
      <div class="errmsg" id="e-tvdb_key">Required</div>
    </div>
    <div class="field">
      <label>OMDb API Key <span class="opt">(optional)</span></label>
      <input type="password" id="omdb_key" placeholder="Your OMDb API key">
      <div class="hint">Optional last-resort fallback. Free tier at omdbapi.com.</div>
    </div>
  </div>

  <div id="s3" style="display:none">
    <div class="field">
      <label>AI Verification Backend <span class="opt">(optional)</span></label>
      <select id="llm_backend" onchange="onLLM()">
        <option value="">Disabled (low-confidence files go to Review Queue)</option>
        <option value="anthropic">Anthropic (Claude)</option>
        <option value="openai">OpenAI</option>
        <option value="ollama">Ollama (local)</option>
      </select>
      <div class="hint">Without an LLM, ambiguous matches surface in the Review Queue for manual review.</div>
    </div>
    <div id="llm-keyed" style="display:none">
      <div class="field">
        <label>API Key <span class="req">*</span></label>
        <input type="password" id="llm_api_key" placeholder="sk-...">
        <div class="errmsg" id="e-llm_api_key">Required when an AI backend is selected</div>
      </div>
      <div class="field">
        <label>Model <span class="opt">(optional)</span></label>
        <input type="text" id="llm_model" placeholder="e.g. claude-sonnet-4-6 or gpt-4o">
      </div>
    </div>
    <div id="llm-ollama" style="display:none">
      <div class="field">
        <label>Ollama Host</label>
        <input type="text" id="ollama_host" value="http://localhost:11434">
      </div>
      <div class="field">
        <label>Model</label>
        <input type="text" id="ollama_model" placeholder="e.g. llama3">
      </div>
    </div>
  </div>

  <div id="success">
    <div class="icon">&#10003;</div>
    <h2>Setup Complete</h2>
    <p>MediaForge is configured. Redirecting to the main interface...</p>
  </div>

  <div class="actions" id="actions">
    <button class="btn btn-secondary" id="btn-back" onclick="back()" style="display:none">Back</button>
    <button class="btn btn-primary" id="btn-next" onclick="next()">Next</button>
  </div>
</div>
<script>
var step=1,total=3;
var labels=['Folders','API Keys','AI Verification'];
var descs=[
  'Configure where MediaForge watches for new files and stores your library.',
  'API keys are used to identify movies and TV shows by title and year.',
  'Optionally configure an AI backend to verify ambiguous metadata matches.'
];

function g(id){return document.getElementById(id);}
function v(id){return g(id).value.trim();}
function show(id){g(id).style.display='';}
function hide(id){g(id).style.display='none';}

function updateUI(){
  for(var i=1;i<=total;i++){
    var b=g('b'+i);
    b.className='bar'+(i<step?' done':i===step?' active':'');
  }
  g('lbl').innerHTML='<strong>Step '+step+' of '+total+': '+labels[step-1]+'</strong>'+descs[step-1];
  step>1?show('btn-back'):hide('btn-back');
  g('btn-next').textContent=step===total?'Finish':'Next';
}

function onLLM(){
  var b=v('llm_backend');
  g('llm-keyed').style.display=(b==='anthropic'||b==='openai')?'':'none';
  g('llm-ollama').style.display=b==='ollama'?'':'none';
}

function setErr(id,msg){
  var el=g(id);if(el&&el.tagName==='INPUT'){if(!el.className.includes('error'))el.className+=' error';}
  var em=g('e-'+id);if(em){em.textContent=msg||'Required';em.style.display='';}
}
function clrErr(id){
  var el=g(id);if(el)el.className=el.className.replace(/ *error/g,'');
  var em=g('e-'+id);if(em)em.style.display='none';
}

function validate(){
  var ok=true;
  if(step===1){
    ['watch_folder','movies_library','tvshows_library'].forEach(function(id){
      if(!v(id)){setErr(id);ok=false;}else clrErr(id);
    });
  }else if(step===2){
    ['tmdb_key','tvdb_key'].forEach(function(id){
      if(!v(id)){setErr(id);ok=false;}else clrErr(id);
    });
  }else if(step===3){
    var b=v('llm_backend');
    if((b==='anthropic'||b==='openai')&&!v('llm_api_key')){setErr('llm_api_key');ok=false;}
    else clrErr('llm_api_key');
  }
  return ok;
}

function next(){
  if(!validate())return;
  if(step<total){
    hide('s'+step);step++;show('s'+step);updateUI();
  }else{
    submit();
  }
}

function back(){
  if(step>1){hide('s'+step);step--;show('s'+step);updateUI();}
}

function submit(){
  var banner=g('banner');banner.style.display='none';
  var btn=g('btn-next');btn.disabled=true;btn.textContent='Saving...';
  var b=v('llm_backend');
  var payload={
    watch_folder:v('watch_folder'),
    staging_folder:v('staging_folder'),
    movies_library:v('movies_library'),
    tvshows_library:v('tvshows_library'),
    tmdb_key:v('tmdb_key'),
    tvdb_key:v('tvdb_key'),
    omdb_key:v('omdb_key'),
    llm_backend:b,
    llm_api_key:v('llm_api_key'),
    llm_model:b==='ollama'?v('ollama_model'):v('llm_model'),
    ollama_host:v('ollama_host')
  };
  fetch('/api/setup',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)})
    .then(function(r){if(!r.ok)return r.text().then(function(t){throw new Error(t);});return r.json();})
    .then(function(){
      hide('s3');hide('actions');show('success');
      for(var i=1;i<=total;i++)g('b'+i).className='bar done';
      setTimeout(function(){window.location.href='/';},2000);
    })
    .catch(function(e){
      btn.disabled=false;btn.textContent='Finish';
      banner.textContent='Setup failed: '+e.message;banner.style.display='';
    });
}
</script>
</body>
</html>`
