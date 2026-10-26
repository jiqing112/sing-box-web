/* architecture doc — syntax highlight, toc spy, copy */

const KEYWORDS = new Set([
  'package', 'import', 'func', 'return', 'if', 'else', 'for', 'range', 'var', 'const', 'type',
  'struct', 'interface', 'map', 'chan', 'go', 'defer', 'select', 'switch', 'case', 'default',
  'break', 'continue', 'nil', 'true', 'false', 'error', 'string', 'int', 'int64', 'uint64',
  'bool', 'byte', 'any', 'fallthrough', 'goto',
  'export', 'let', 'async', 'await', 'from', 'new', 'class', 'extends', 'typeof', 'as', 'of',
  'in', 'throw', 'try', 'catch', 'finally', 'function', 'this', 'void', 'null', 'undefined',
  'readonly', 'declare', 'implements', 'public', 'private', 'static'
]);

function esc(s) {
  return s.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c]));
}

function highlight(code, lang) {
  const commentRe = lang === 'nft' || lang === 'sh'
    ? /(#[^\n]*)/
    : /(\/\/[^\n]*|\/\*[\s\S]*?\*\/)/;
  const re = new RegExp(
    commentRe.source +
    '|("(?:\\\\.|[^"\\\\])*"|\'(?:\\\\.|[^\'\\\\])*\'|`(?:\\\\.|[^`\\\\])*`)' +
    '|(<\\/?)([A-Za-z][\\w-]*)' +
    '|([A-Za-z_$][\\w$]*)' +
    '|(\\d+(?:\\.\\d+)?)',
    'g'
  );
  let out = '', last = 0, m;
  while ((m = re.exec(code))) {
    out += esc(code.slice(last, m.index));
    if (m[1] !== undefined) {
      out += `<span class="tok-c">${esc(m[1])}</span>`;
    } else if (m[2] !== undefined) {
      let j = re.lastIndex;
      while (code[j] === ' ' || code[j] === '\t') j++;
      const isKey = code[j] === ':';
      out += `<span class="${isKey ? 'tok-k' : 'tok-s'}">${esc(m[2])}</span>`;
    } else if (m[3] !== undefined) {
      out += `<span class="tok-k">${esc(m[3])}</span><span class="tok-t">${esc(m[4])}</span>`;
    } else if (m[5] !== undefined) {
      const w = m[5];
      if (KEYWORDS.has(w)) out += `<span class="tok-k">${w}</span>`;
      else if (/^[A-Z]/.test(w)) out += `<span class="tok-t">${w}</span>`;
      else out += esc(w);
    } else if (m[6] !== undefined) {
      out += `<span class="tok-n">${m[6]}</span>`;
    }
    last = re.lastIndex;
  }
  return out + esc(code.slice(last));
}

document.querySelectorAll('pre code').forEach((el) => {
  const lang = el.dataset.lang || '';
  const raw = el.textContent;
  el.innerHTML = highlight(raw, lang);
  el.dataset.raw = raw;
});

document.querySelectorAll('.code-hd .copy').forEach((btn) => {
  btn.addEventListener('click', () => {
    const code = btn.closest('.code-block').querySelector('code');
    const text = code.dataset.raw || code.textContent;
    navigator.clipboard?.writeText(text);
    const old = btn.textContent;
    btn.textContent = '已复制';
    setTimeout(() => (btn.textContent = old), 1400);
  });
});

/* progress bar */
const bar = document.querySelector('.progress');
const onScroll = () => {
  const h = document.documentElement;
  const max = h.scrollHeight - h.clientHeight;
  bar.style.width = max > 0 ? `${(h.scrollTop / max) * 100}%` : '0';
};
document.addEventListener('scroll', onScroll, { passive: true });
onScroll();

/* toc scrollspy */
const links = [...document.querySelectorAll('.toc a')];
const sections = links
  .map((a) => document.querySelector(a.getAttribute('href')))
  .filter(Boolean);

const spy = new IntersectionObserver(
  (entries) => {
    entries.forEach((e) => {
      if (!e.isIntersecting) return;
      const id = e.target.id;
      links.forEach((a) => a.classList.toggle('on', a.getAttribute('href') === `#${id}`));
    });
  },
  { rootMargin: '-15% 0px -70% 0px' }
);
sections.forEach((s) => spy.observe(s));
