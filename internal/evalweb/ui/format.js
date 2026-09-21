'use strict';
// Render a conservative Markdown subset with DOM nodes only. Model text is
// untrusted: raw HTML is always text and links allow only http(s).
function markdown(text) {
  const root=document.createElement('div');root.className='markdown';
  const inline=(el,text)=>{
    const token=/(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\(https?:\/\/[^\s)]+\))/g;
    let at=0;
    for(const match of text.matchAll(token)) {
      el.append(document.createTextNode(text.slice(at,match.index)));
      const t=match[0];let node;
      if(t.startsWith('`')){node=document.createElement('code');node.textContent=t.slice(1,-1);}
      else if(t.startsWith('**')){node=document.createElement('strong');node.textContent=t.slice(2,-2);}
      else{const parts=/^\[([^\]]+)\]\((.+)\)$/.exec(t);node=document.createElement('a');node.textContent=parts[1];node.href=parts[2];node.rel='noopener noreferrer';node.target='_blank';}
      el.append(node);at=match.index+t.length;
    }
    el.append(document.createTextNode(text.slice(at)));
  };
  let code=null, list=null;
  for(const line of String(text??'').split('\n')) {
    if(line.startsWith('```')) {
      if(code){code=null;}else{const pre=document.createElement('pre');code=document.createElement('code');pre.append(code);root.append(pre);if(line.slice(3).trim())pre.setAttribute('aria-label',line.slice(3).trim()+' code');}
      list=null;continue;
    }
    if(code){code.textContent+=line+'\n';continue;}
    if(!line.trim()){list=null;continue;}
    const bullet=/^\s*(?:[-*]|\d+\.)\s+(.+)$/.exec(line);
    if(bullet){if(!list){list=document.createElement(/^\s*\d/.test(line)?'ol':'ul');root.append(list);}const li=document.createElement('li');inline(li,bullet[1]);list.append(li);continue;}
    list=null;
    const heading=/^(#{1,6})\s+(.+)$/.exec(line);
    const el=document.createElement(heading?'h'+Math.min(heading[1].length+2,6):line.startsWith('> ')?'blockquote':'p');
    inline(el,heading?heading[2]:line.startsWith('> ')?line.slice(2):line);root.append(el);
  }
  return root;
}
