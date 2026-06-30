"""Localhost FastAPI app + WebUI for the Claim-drafting workflow (ADR 0005).

Short-lived per session: ``qatlas contrib claim <paper>`` builds the SessionState
(paper bytes already fetched), starts this app on localhost, opens a browser, and
the human drives the loop — draft (agent or manual) → edit → resolve references →
Confirm. Each **Confirm** files exactly ONE ``type/theorem`` gitea issue.

FastAPI / uvicorn are an optional dependency (``qatlas[contrib]``); imported
lazily so the rest of the client never pays for them.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from pydantic import BaseModel

from .drafter import ClaimDrafter
from .gitea import GiteaClient, GiteaError
from .issue import CONTRIB_LABELS, render_body, render_title
from .models import ClaimDraft, PaperRef, ResolvedRef
from .references import resolve_references


@dataclass
class SessionState:
    paper: PaperRef
    markdown: str
    base_url: str
    auth_headers: dict[str, str]
    verify: bool
    gitea: GiteaClient
    drafter: ClaimDrafter
    mailto: str | None = None
    # html_url of issues filed this session, by claim_id.
    filed: dict[str, str] = field(default_factory=dict)


# Request bodies — module level so FastAPI resolves them as request bodies
# (a closure-local Pydantic model is mis-read as a scalar query param).
class RefsIn(BaseModel):
    refs: list[str]


class DraftIn(BaseModel):
    hint: str = ""


class CheckIn(BaseModel):
    claim_id: str


class ConfirmIn(BaseModel):
    claim: ClaimDraft
    resolved: list[ResolvedRef] = []


def build_app(state: SessionState):
    """Construct the FastAPI app bound to ``state``. Lazy fastapi import."""
    from fastapi import FastAPI
    from fastapi.responses import HTMLResponse, JSONResponse

    app = FastAPI(title="qatlas contrib claim")

    @app.get("/", response_class=HTMLResponse)
    def index() -> str:
        return INDEX_HTML

    @app.get("/api/session")
    def session() -> dict:
        return {
            "paper": state.paper.model_dump(),
            "markdown_chars": len(state.markdown),
            "agent_available": state.drafter.available,
            "backend": state.drafter.backend_name,
            "gitea_repo": state.gitea._cfg.repo,
        }

    @app.get("/api/paper-markdown", response_class=HTMLResponse)
    def paper_markdown() -> str:
        return state.markdown

    @app.post("/api/draft")
    def draft(body: DraftIn):
        if not state.drafter.available:
            return JSONResponse(
                {"detail": "no LLM backend configured — draft manually"}, status_code=400
            )
        try:
            drafts = state.drafter.draft(state.markdown, body.hint)
        except Exception as exc:  # noqa: BLE001 — surface any LLM error to the UI
            return JSONResponse({"detail": f"draft failed: {exc}"}, status_code=502)
        return {"drafts": [d.model_dump() for d in drafts]}

    @app.post("/api/resolve-refs")
    def resolve(body: RefsIn):
        resolved = resolve_references(
            state.base_url, state.auth_headers, state.verify, body.refs, mailto=state.mailto
        )
        return {"resolved": [r.model_dump() for r in resolved]}

    @app.post("/api/preview")
    def preview(body: ConfirmIn):
        return {
            "title": render_title(body.claim),
            "body": render_body(body.claim, body.resolved or None),
        }

    @app.post("/api/check")
    def check(body: CheckIn):
        try:
            existing = state.gitea.find_open_issue_by_claim_id(body.claim_id)
        except GiteaError as exc:
            return JSONResponse({"detail": str(exc)}, status_code=502)
        if existing:
            return {"exists": True, "url": existing.get("html_url"), "number": existing.get("number")}
        return {"exists": False}

    @app.post("/api/confirm")
    def confirm(body: ConfirmIn):
        claim = body.claim
        # Idempotency (DECIDED — refuse + show URL): an OPEN issue with the same
        # claim_id means the daemon may be mid-proof; do not duplicate / update.
        try:
            existing = state.gitea.find_open_issue_by_claim_id(claim.claim_id)
        except GiteaError as exc:
            return JSONResponse({"detail": str(exc)}, status_code=502)
        if existing:
            return JSONResponse(
                {
                    "detail": "an open issue already carries this claim_id",
                    "existing_url": existing.get("html_url"),
                    "existing_number": existing.get("number"),
                },
                status_code=409,
            )
        title = render_title(claim)
        text = render_body(claim, body.resolved or None)
        try:
            issue = state.gitea.create_issue(title, text, CONTRIB_LABELS)
        except GiteaError as exc:
            return JSONResponse({"detail": str(exc)}, status_code=502)
        url = issue.get("html_url", "")
        state.filed[claim.claim_id] = url
        return {"number": issue.get("number"), "html_url": url}

    return app


def run(state: SessionState, host: str, port: int) -> None:
    """Block, serving the app with uvicorn. Lazy import."""
    import uvicorn

    uvicorn.run(build_app(state), host=host, port=port, log_level="warning")


# The whole UI in one file — vanilla JS, no build step. Kept compact: the human
# reviews/edits each drafted Claim and clicks "Confirm claim" / "确认 claim".
INDEX_HTML = """<!doctype html>
<html lang="en"><head><meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>qatlas contrib claim</title>
<style>
 :root{color-scheme:light dark}
 body{font:14px/1.5 system-ui,sans-serif;margin:0;background:#0b0e14;color:#e6e6e6}
 header{padding:16px 24px;border-bottom:1px solid #232a36;background:#11151f}
 h1{font-size:16px;margin:0 0 4px} .muted{color:#8a93a6;font-size:12px}
 main{max-width:980px;margin:0 auto;padding:24px}
 .row{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:16px}
 button{background:#2b6cff;color:#fff;border:0;border-radius:6px;padding:8px 14px;font:inherit;cursor:pointer}
 button.ghost{background:#222a39;color:#cfe} button:disabled{opacity:.5;cursor:not-allowed}
 input,textarea{width:100%;box-sizing:border-box;background:#0e1320;color:#e6e6e6;border:1px solid #2a3242;border-radius:6px;padding:8px;font:inherit}
 textarea{min-height:64px;resize:vertical}
 .card{border:1px solid #232a36;border-radius:10px;padding:16px;margin-bottom:16px;background:#11151f}
 label{display:block;font-size:12px;color:#8a93a6;margin:8px 0 2px}
 .ref{font-family:ui-monospace,monospace;font-size:12px}
 .ok{color:#5ad19a} .warn{color:#e6b450} .err{color:#ff6b6b}
 a{color:#7aa2ff}
</style></head><body>
<header>
 <h1>qatlas contrib claim</h1>
 <div class="muted" id="meta">loading…</div>
</header>
<main>
 <div class="row">
  <input id="hint" placeholder="optional focus hint for the agent (e.g. 'main theorem only')" style="flex:1"/>
  <button id="draftBtn">Draft with agent</button>
  <button id="addBtn" class="ghost">Add empty claim</button>
 </div>
 <div id="claims"></div>
</main>
<script>
let S={paper:{},agent:false,repo:""};
const $=s=>document.querySelector(s);
async function jget(u){const r=await fetch(u);return r.json()}
async function jpost(u,b){const r=await fetch(u,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(b)});return {ok:r.ok,status:r.status,data:await r.json().catch(()=>({}))}}
function el(t,a={},...c){const e=document.createElement(t);for(const k in a){if(k==='class')e.className=a[k];else if(k==='html')e.innerHTML=a[k];else e[k]=a[k]}for(const x of c)e.append(x);return e}

function claimCard(c){
 c=c||{claim_id:'',paper:S.paper,claim_kind:'theorem',natural_language:'',latex:'',why_it_matters:'',source_md_lines:'',references:[]};
 const card=el('div',{class:'card'});
 const slug=el('input',{value:(c.claim_id.split(':').slice(1).join(':'))||'',placeholder:'slug (=> claim_id <paper>:<slug>)'});
 const kind=el('input',{value:c.claim_kind||'theorem'});
 const nl=el('textarea',{value:c.natural_language||''});
 const tex=el('textarea',{value:c.latex||''});
 const why=el('textarea',{value:c.why_it_matters||''});
 const src=el('input',{value:c.source_md_lines||''});
 const refs=el('textarea',{value:(c.references||[]).join('\\n'),placeholder:'one kind:id per line\\narxiv:2208.06941'});
 const out=el('div',{class:'muted'});
 const refsOut=el('div',{class:'muted'});
 function collect(){return {claim_id:S.paper.id+':'+(slug.value.trim()||'claim'),paper:S.paper,claim_kind:kind.value.trim()||'theorem',natural_language:nl.value,latex:tex.value||null,why_it_matters:why.value||null,source_md_lines:src.value||null,references:refs.value.split('\\n').map(x=>x.trim()).filter(Boolean)}}
 let resolved=[];
 const resolveBtn=el('button',{class:'ghost',textContent:'Resolve references'});
 resolveBtn.onclick=async()=>{const c2=collect();const r=await jpost('/api/resolve-refs',{refs:c2.references});resolved=r.data.resolved||[];refsOut.innerHTML=resolved.map(x=>'<span class=ref>'+x.ref+'</span> '+(x.resolved?('<span class=ok>'+(x.title||'')+(x.year?' ('+x.year+')':'')+'</span>'+(x.hosted?' · hosted':'')):'<span class=warn>⚠ unresolved</span>')).join('<br>')};
 const previewBtn=el('button',{class:'ghost',textContent:'Preview issue'});
 previewBtn.onclick=async()=>{const r=await jpost('/api/preview',{claim:collect(),resolved});out.innerHTML='<b>'+r.data.title+'</b><pre style="white-space:pre-wrap">'+(r.data.body||'').replace(/</g,'&lt;')+'</pre>'};
 const confirmBtn=el('button',{textContent:'Confirm claim / 确认 claim'});
 confirmBtn.onclick=async()=>{confirmBtn.disabled=true;const r=await jpost('/api/confirm',{claim:collect(),resolved});
   if(r.ok){out.innerHTML='<span class=ok>filed #'+r.data.number+' → <a href="'+r.data.html_url+'" target=_blank>'+r.data.html_url+'</a></span>'}
   else if(r.status===409){out.innerHTML='<span class=warn>refused: open issue already exists → <a href="'+r.data.existing_url+'" target=_blank>'+r.data.existing_url+'</a></span>';confirmBtn.disabled=false}
   else{out.innerHTML='<span class=err>'+(r.data.detail||('HTTP '+r.status))+'</span>';confirmBtn.disabled=false}};
 card.append(el('label',{textContent:'slug'}),slug,el('label',{textContent:'kind'}),kind,
  el('label',{textContent:'natural language (near-verbatim)'}),nl,
  el('label',{textContent:'latex'}),tex,
  el('label',{textContent:'why it matters'}),why,
  el('label',{textContent:'source-md lines'}),src,
  el('label',{textContent:'references (kind:id per line)'}),refs,
  el('div',{class:'row'},resolveBtn,previewBtn,confirmBtn),refsOut,out);
 return card;
}
function addCard(c){$('#claims').append(claimCard(c))}
$('#addBtn').onclick=()=>addCard();
$('#draftBtn').onclick=async()=>{$('#draftBtn').disabled=true;$('#draftBtn').textContent='Drafting…';
 const r=await jpost('/api/draft',{hint:$('#hint').value});$('#draftBtn').disabled=false;$('#draftBtn').textContent='Draft with agent';
 if(!r.ok){alert(r.data.detail||'draft failed');return}
 (r.data.drafts||[]).forEach(addCard)};
(async()=>{S=Object.assign(S,await jget('/api/session'));S.paper=S.paper||{};
 $('#meta').textContent='paper '+(S.paper.id||'?')+' · '+(S.markdown_chars||0)+' md chars · agent: '+S.backend+' · → '+S.gitea_repo;
 $('#draftBtn').disabled=!S.agent_available;
 if(!S.agent_available)$('#draftBtn').title='no LLM key — add claims manually';
})();
</script></body></html>
"""
