'use strict';
// GFM, including tables and task lists. Model HTML remains visible text;
// sanitization is a second boundary for generated markup and unsafe URLs.
function markdown(text) {
  const root = document.createElement('div');
  root.className = 'markdown';
  const escapeHTML = value => value.replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const renderer = new marked.Renderer();
  renderer.html = ({text}) => escapeHTML(text);
  root.innerHTML = DOMPurify.sanitize(marked.parse(String(text ?? ''), {gfm:true, renderer}), {
    USE_PROFILES:{html:true}, FORBID_TAGS:['img','video','audio','iframe','style','form'],
    FORBID_ATTR:['style','id','name'],
  });
  root.querySelectorAll('a').forEach(link => {
    if (!/^https?:\/\//i.test(link.getAttribute('href') || '')) link.removeAttribute('href');
    else { link.target='_blank'; link.rel='noopener noreferrer'; }
  });
  root.querySelectorAll('table').forEach(table => {
    const scroll=document.createElement('div');scroll.className='markdown-table';
    table.before(scroll);scroll.append(table);
  });
  return root;
}
