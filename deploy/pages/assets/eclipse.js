(()=>{var Ne=7500;var dt=13,pt=1.2307692307692308,bt=0.6;function Ie(t,n){return Math.min(t>0?t:1,n)}function Ce(t,n,o,r,s=7500,l=dt,e=pt){let a=Math.max(1,Math.round(t*o)),d=Math.max(1,Math.round(n*o)),u=Math.max(l,Math.ceil(Math.sqrt(t*n/(s*bt*e))));for(;;){let x=Math.max(4,Math.round(r(Math.round(u*o)))),b=Math.max(4,Math.round(u*e*o)),v=Math.ceil(a/x),g=Math.ceil(d/b);if(v*g<=s||u>=400)return{W:a,H:d,dpr:o,fontPx:u,cw:x,ch:b,cols:v,rows:g};u+=1}}var C={min:0.8,feather:40,pad:0},Be={min:0.07,feather:70,pad:30};function Z(t){return Math.min(1,Math.max(0.4,t/1100))}function ue(t,n,o,r=1){return{x0:t.left*n,y0:t.top*n,x1:t.right*n,y1:t.bottom*n,min:o.min,feather:o.feather*n*r,pad:o.pad*n*r}}function ht(t,n,o){let r=1;for(let s=0;s<o.length;s++){let l=o[s],e=Math.max(l.x0-t,0,t-l.x1),a=Math.max(l.y0-n,0,n-l.y1),d=Math.hypot(e,a)-l.pad;if(d<l.feather){let u=Math.max(d,0)/l.feather;r=Math.min(r,l.min+(1-l.min)*(u*u*(3-2*u)))}}return r}function De(t,n,o,r=1){let{cols:s,rows:l,cw:e,ch:a}=t;for(let d=0;d<l;d++){let u=(d+0.5)*a;for(let x=0;x<s;x++)o[d*s+x]=Math.round(ht((x+0.5)*e,u,n)*r*255)}return o}var fe=720,xt=0.45;function Oe(t,n,o,r,s=fe){let l={cx:t*0.77,cy:n*0.45,r:Math.min(t,n)*0.37,dim:1};if(!(t<n*1.05||t/o<s)||!r)return l;return{cx:t/2,cy:(r.top+r.bottom)/2,r:Math.min(t*0.46,n*0.3),dim:xt}}function Ue(t,n,o,r=1){let s=0;for(let[,u]of t)s=Math.max(s,u);let l=new Set,e=[];for(let u=1;u<n;u++){let x=(u/(n-1))**r*s,b=[];for(let v=0;v<o;v++){let g=null,R=Number.POSITIVE_INFINITY;for(let h of t){if(l.has(h[0]))continue;let D=Math.abs(h[1]-x);if(D<R)R=D,g=h}if(!g)break;l.add(g[0]),b.push(g)}if(b.length===0)break;while(b.length<o)b.push(b[0]);b.sort((v,g)=>v[1]-g[1]),e.push(b)}let a=(u)=>u.reduce((x,b)=>x+b[1],0)/u.length;e.sort((u,x)=>a(u)-a(x));let d=Array.from({length:o},()=>" ");for(let u of e)for(let x of u)d.push(x[0]);return d}function ze(t,n,o){return t-n>=o-1.5}var j=(t,n,o)=>[t[0]+(n[0]-t[0])*o,t[1]+(n[1]-t[1])*o,t[2]+(n[2]-t[2])*o];var gt="eclipse",Xe=24,vt=".'`:;,-~+=*ottxXoO%S8#@",Et=".'`\":;,_-~^+=*<>|/\\()[]{}#";var B={min:0.05,feather:90,pad:24},me={eclipse:{name:"eclipse",fps:Xe,candidates:vt,levels:12,variants:1,extras:"",extraTone:0.45,gamma:0.7,cutoff:0.045,dither:0.75,maskMode:"level",panel:C,quiet:Be,staticTime:14},"dial-out":{name:"dial-out",fps:20,candidates:".':-=+*",levels:8,variants:1,extras:"",extraTone:0.45,gamma:0.55,cutoff:0.04,dither:0.5,maskMode:"alpha",panel:C,quiet:B,staticTime:32},"lost-signal":{name:"lost-signal",fps:12,candidates:Et,levels:7,variants:3,extras:"",extraTone:0.45,gamma:0.8,cutoff:0.04,dither:0.4,maskMode:"alpha",panel:C,quiet:B,staticTime:9},dome:{name:"dome",fps:5,candidates:".':+*",levels:6,variants:1,extras:"_",extraTone:0.5,gamma:0.5,cutoff:0.05,dither:0.3,maskMode:"alpha",panel:C,quiet:B,staticTime:40},band:{name:"band",fps:10,candidates:".,':+*",levels:7,variants:1,extras:".",extraTone:0.95,gamma:0.5,cutoff:0.04,dither:0.3,maskMode:"alpha",panel:C,quiet:B,staticTime:20}};function qe(t){return t!==void 0&&Object.hasOwn(me,t)?me[t]:me[gt]}function Ve(t,n){let o=n??t.fps;return Math.min(Xe,Math.max(1,Number.isFinite(o)?o:t.fps))}var yt=0.09;var Rt=0.46,Mt=0.44,Lt=0.12,At=0.16,_=(t,n,o)=>Math.min(o,Math.max(n,t));function He(t,n){return(B.pad+B.feather)*n*Z(t/n)}function St(t,n){let o=t?[t,...n]:n;if(o.length===0)return null;return{left:Math.min(...o.map((r)=>r.left)),right:Math.max(...o.map((r)=>r.right))}}var Tt=0.4,_t=0.3;function We(t,n){return 0.15*n+0.025*t}var Pt=1,wt=0.8;function kt(t,n,o,r){let s=We(t,n),l=Pt*n,e=He(t,o),a=wt*t;for(let d of r){if(d.bottom<0||d.top>n)continue;let u=Math.min(d.bottom,n);a=Math.max(a,d.right+e+l*(u/n))}return a=Math.min(a,t+0.5*s),{top:a,bottom:a-l}}function $e(t,{W:n,H:o,dpr:r,rect:s,quiet:l=[],narrowBelow:e=fe,focus:a}){let d={cx:0,cy:0,r:0,dim:1},u=St(s,l),x=He(n,r);switch(t){case"eclipse":return{orb:Oe(n,o,r,s,e),focus:[0,0,0,0]};case"dial-out":{let b=n/r<e,v=0.74;if(!b&&u){let R=112*r;v=(u.left+u.right)/2<n/2?Math.max(0.74,(u.right+x+R)/n):Math.min(0.26,(u.left-x-R)/n)}let g=a??{x:v,y:Tt};return{orb:d,focus:[_(g.x,0.08,0.92)*n,_(g.y,0.08,0.8)*o,0,0]}}case"dome":{let b=0.76;if(u&&(u.left+u.right)/2<n/2)b=Math.max(b,(u.right+x)/n);let v=a??{x:b,y:_t};return{orb:d,focus:[1.02*n,-0.02*o,_(v.x,0.1,0.94)*n,_(v.y,0.1,0.8)*o]}}case"band":{let b=kt(n,o,r,s?[s,...l]:l);return{orb:d,focus:[b.top,b.bottom,We(n,o),0]}}case"lost-signal":{let b=yt*Math.min(n,o),v=n/2,g=o/2,R=n*0.28,h=o*0.3;if(s)v=(s.left+s.right)/2,g=(s.top+s.bottom)/2,R=(s.right-s.left)/2+b+n*0.03,h=(s.bottom-s.top)/2+b+o*0.05;return R=_(R,n*Lt,n*Rt),h=_(h,o*At,o*Mt),v=_(v,R,Math.max(R,n-R)),g=_(g,h,Math.max(h,o-h)),{orb:d,focus:[v,g,R,h]}}default:return{orb:d,focus:[0,0,0,0]}}}var de=`#version 300 es
void main(){ vec2 p = vec2(float((gl_VertexID<<1)&2), float(gl_VertexID&2)); gl_Position = vec4(p*2.0-1.0, 0.0, 1.0); }`;function Ke(t){return`#version 300 es
precision highp float; precision highp int;
uniform float uT;
uniform vec2 uRes;
uniform vec2 uGrid;
uniform vec2 uCell;
/* Orb centre (xy) and radius (z), device px. */
uniform vec3 uOrb;
/* The scene's anchor, device px. */
uniform vec4 uFocus;
/* 1 when the mask lowers opacity, 0 when it lowers the tone. */
uniform float uMaskAlpha;
uniform sampler2D uMask;
float hash2(ivec2 p){ uint h = uint(p.x)*374761393u ^ uint(p.y)*668265263u; h = (h ^ (h>>13u))*1274126177u; return float(h ^ (h>>16u))/4294967295.0; }
float hash3(ivec2 p, int k){ uint h = uint(p.x)*374761393u ^ uint(p.y)*668265263u ^ uint(k)*2246822519u; h = (h ^ (h>>13u))*1274126177u; return float(h ^ (h>>16u))/4294967295.0; }
`+t+`
out vec4 o;
void main(){
	ivec2 cr = ivec2(gl_FragCoord.xy);
	ivec2 cell = ivec2(cr.x, int(uGrid.y) - 1 - cr.y);
	vec2 px = (vec2(cell) + 0.5) * uCell;
	vec4 s = field(cell, px, uT);
	float m = texelFetch(uMask, cell, 0).r;
	float lm = mix(m, 1.0, uMaskAlpha);
	float am = mix(1.0, m, uMaskAlpha) * s.w;
	o = vec4(s.x * lm, s.y / 255.0, am, s.z / 255.0);
}`}var Ye=`#version 300 es
precision highp float; precision highp int;
uniform vec2 uRes;
uniform vec2 uGrid;
uniform vec2 uCell;
uniform sampler2D uField;
uniform sampler2D uAtlas;
uniform float uLevels;
/* Glyphs per tone level. */
uniform float uVariants;
/* First atlas column of the extra symbols (levels x variants). */
uniform float uExtraBase;
uniform float uCut;
uniform float uDither;
out vec4 o;
const float BAYER[16] = float[16](0.,8.,2.,10.,12.,4.,14.,6.,3.,11.,1.,9.,15.,7.,13.,5.);
void main(){
	vec2 p = vec2(gl_FragCoord.x, uRes.y - gl_FragCoord.y);
	ivec2 c = ivec2(floor(p / uCell));
	ivec2 fr = ivec2(c.x, int(uGrid.y) - 1 - c.y);
	vec4 f = texelFetch(uField, fr, 0);
	int hue = int(f.g * 255.0 + 0.5);
	int sel = int(f.a * 255.0 + 0.5);
	int col = -1;
	if (sel >= 128) {
		col = int(uExtraBase) + sel - 128;
	} else if (f.r >= uCut) {
		float b = BAYER[((c.y & 3) << 2) | (c.x & 3)] / 16.0 - 0.47;
		int idx = clamp(int(floor(f.r * (uLevels - 1.0) + 0.5 + b * uDither)), 0, int(uLevels) - 1);
		if (idx > 0) col = idx * int(uVariants) + sel % int(uVariants);
	}
	vec4 px4 = vec4(0.0);
	if (col >= 0) {
		ivec2 cs = ivec2(uCell);
		ivec2 loc = ivec2(p) - c * cs;
		px4 = texelFetch(uAtlas, ivec2(col * cs.x + loc.x, hue * cs.y + loc.y), 0) * f.b;
	}
	o = px4;
}`;var pe='"IBM Plex Mono", ui-monospace, "SFMono-Regular", Menlo, monospace',Ft=120,Gt=2500,J=900;function Qe(t,n,o){let r=t.createShader(n);if(!r)throw Error("createShader failed");if(t.shaderSource(r,o),t.compileShader(r),!t.getShaderParameter(r,t.COMPILE_STATUS))throw Error(t.getShaderInfoLog(r)??"shader compile failed");return r}function Ze(t,n,o){let r=t.createProgram();if(!r)throw Error("createProgram failed");let s=Qe(t,t.VERTEX_SHADER,n),l=Qe(t,t.FRAGMENT_SHADER,o);if(t.attachShader(r,s),t.attachShader(r,l),t.linkProgram(r),t.deleteShader(s),t.deleteShader(l),!t.getProgramParameter(r,t.LINK_STATUS))throw Error(t.getProgramInfoLog(r)??"program link failed");return r}function je(t,n){let o=Ze(t,de,Ke(n)),r=Ze(t,de,Ye),s=t.createVertexArray(),l=(a)=>t.getUniformLocation(o,a),e=(a)=>t.getUniformLocation(r,a);return{field:o,blit:r,vao:s,fT:l("uT"),fRes:l("uRes"),fGrid:l("uGrid"),fCell:l("uCell"),fOrb:l("uOrb"),fFocus:l("uFocus"),fMaskAlpha:l("uMaskAlpha"),fMask:l("uMask"),bRes:e("uRes"),bGrid:e("uGrid"),bCell:e("uCell"),bField:e("uField"),bAtlas:e("uAtlas"),bLevels:e("uLevels"),bVariants:e("uVariants"),bExtraBase:e("uExtraBase"),bCut:e("uCut"),bDither:e("uDither")}}var Je=0,et=1,tt=2;function nt(t,n,o){let r=qe(n.scene),s=o[r.name];if(s===void 0)return console.warn(`ascii scene "${r.name}" is not in this bundle`),null;let l=t.getContext("webgl2",{alpha:!0,premultipliedAlpha:!0,antialias:!1,depth:!1,stencil:!1,powerPreference:"low-power",preserveDrawingBuffer:!1});if(!l)return null;let e=l,a;try{a=je(e,s)}catch(i){return console.warn("ascii backdrop disabled:",i),null}let d=n.fontFamily??pe,u=n.staticTime??r.staticTime,x=n.dprCap??2,b=(i,f)=>typeof n.maxCells==="function"?n.maxCells(i,f):n.maxCells??Ne,v=1000/Ve(r,n.fps),g=n.reducedMotion===void 0&&typeof matchMedia==="function"?matchMedia("(prefers-reduced-motion: reduce)"):null,R=()=>n.reducedMotion??g?.matches??!1,h=null,D=0,xe=0,ge=null,ve=null,O=null,V=null,H=null,S=null,W=!1,K=null,Y=!1,F=0,ee=0,te=0,Ee=u,ye=u,Re=!0,P=!1,G=!1,Me=!1,ne=0,U=null,z=n.stats===!0,re=new Float64Array(z?J:0),oe=new Float64Array(z?J:0),ie=0;function ae(i){let f=e.createTexture();return e.activeTexture(e.TEXTURE0+i),e.bindTexture(e.TEXTURE_2D,f),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_MIN_FILTER,e.NEAREST),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_MAG_FILTER,e.NEAREST),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_WRAP_S,e.CLAMP_TO_EDGE),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_WRAP_T,e.CLAMP_TO_EDGE),f}function it(){let f=document.createElement("canvas");f.width=f.height=40;let m=f.getContext("2d",{willReadFrequently:!0}),y=Array.from(r.extras);if(!m){let p=Array.from(r.candidates),E=Array.from({length:(r.levels-1)*r.variants},(T,L)=>p[L%p.length]);return[...Array.from({length:r.variants},()=>" "),...E,...y]}m.font=`400 32px ${d}`,m.textAlign="center",m.textBaseline="middle",m.fillStyle="#fff";let c=[];for(let p of r.candidates){m.clearRect(0,0,40,40),m.fillText(p,20,20);let E=m.getImageData(0,0,40,40).data,T=0;for(let L=3;L<E.length;L+=4)T+=E[L]??0;c.push([p,T/255/1600])}return[...Ue(c,r.levels,r.variants),...y]}function at(i,f){let m=n.palette,y=[{dim:m.dim,bright:m.ink},{dim:j(m.dim,m.accent,0.55),bright:j(m.ink,m.accentInk,0.7)},{dim:m.ink,bright:m.star}],c=ve??document.createElement("canvas");ve=c,c.width=i.cw*f.length,c.height=i.ch*y.length;let p=c.getContext("2d");if(!p)return c;let E=Math.round(i.fontPx*i.dpr);p.font=`400 ${E}px ${d}`,p.textBaseline="alphabetic";let T=p.measureText("M"),L=T.fontBoundingBoxAscent||E*1.03,N=T.fontBoundingBoxDescent||E*0.27,Q=Math.round((i.ch-(L+N))/2+L),I=f.length-r.extras.length,w=I/r.variants-1;for(let M=0;M<y.length;M++){let ke=y[M];for(let k=0;k<f.length;k++){let Fe=Math.floor(k/r.variants);if(k<I&&Fe===0)continue;let mt=k<I?(Fe/w)**r.gamma:r.extraTone,le=j(ke.dim,ke.bright,mt);p.fillStyle=`rgb(${le[0]|0},${le[1]|0},${le[2]|0})`;let Ge=f[k];p.fillText(Ge,k*i.cw+Math.round((i.cw-p.measureText(Ge).width)/2),M*i.ch+Q)}}return c}function st(i){let f=t.getBoundingClientRect(),m=(E)=>({left:E.left-f.left,top:E.top-f.top,right:E.right-f.left,bottom:E.bottom-f.top}),y=[],c=n.maskRect?.(),p=Z(t.clientWidth);if(c)y.push(ue(m(c),i.dpr,r.panel,p));for(let E of n.quietRects?.()??[])y.push(ue(m(E),i.dpr,r.quiet,p));return y}function ct(){if(U?.removeEventListener("change",q),U=null,typeof matchMedia!=="function")return;U=matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`),U.addEventListener("change",q,{once:!0})}function se(){if(G||P)return;let{clientWidth:i,clientHeight:f}=t;if(i===0||f===0){h=null;return}let m=Ie(window.devicePixelRatio,x),y=document.createElement("canvas").getContext("2d"),c=Ce(i,f,m,(M)=>{if(!y)return M*0.6;return y.font=`400 ${M}px ${d}`,y.measureText("M").width},b(i,f));h=c,D=i,xe=f,t.width=c.W,t.height=c.H,ge??=it();let p=ge,E=(p.length-r.extras.length)/r.variants,T=at(c,p);for(let M of[O,V,H])if(M)e.deleteTexture(M);if(S)e.deleteFramebuffer(S);let L=st(c),N=t.getBoundingClientRect(),Q=(M)=>({left:(M.left-N.left)*m,top:(M.top-N.top)*m,right:(M.right-N.left)*m,bottom:(M.bottom-N.top)*m}),I=n.maskRect?.()??null,w=$e(r.name,{W:c.W,H:c.H,dpr:m,rect:I?Q(I):null,quiet:(n.quietRects?.()??[]).map(Q),narrowBelow:n.narrowBelow,focus:n.focus});e.pixelStorei(e.UNPACK_ALIGNMENT,1),H=ae(tt),e.texImage2D(e.TEXTURE_2D,0,e.R8,c.cols,c.rows,0,e.RED,e.UNSIGNED_BYTE,De(c,L,new Uint8Array(c.cols*c.rows),w.orb.dim)),O=ae(Je),e.texImage2D(e.TEXTURE_2D,0,e.RGBA8,c.cols,c.rows,0,e.RGBA,e.UNSIGNED_BYTE,null),S=e.createFramebuffer(),e.bindFramebuffer(e.FRAMEBUFFER,S),e.framebufferTexture2D(e.FRAMEBUFFER,e.COLOR_ATTACHMENT0,e.TEXTURE_2D,O,0),e.bindFramebuffer(e.FRAMEBUFFER,null),V=ae(et),e.pixelStorei(e.UNPACK_PREMULTIPLY_ALPHA_WEBGL,!0),e.texImage2D(e.TEXTURE_2D,0,e.RGBA8,e.RGBA,e.UNSIGNED_BYTE,T),e.pixelStorei(e.UNPACK_PREMULTIPLY_ALPHA_WEBGL,!1),e.useProgram(a.field),e.uniform2f(a.fRes,c.W,c.H),e.uniform2f(a.fGrid,c.cols,c.rows),e.uniform2f(a.fCell,c.cw,c.ch),e.uniform3f(a.fOrb,w.orb.cx,w.orb.cy,w.orb.r),e.uniform4f(a.fFocus,...w.focus),e.uniform1f(a.fMaskAlpha,r.maskMode==="alpha"?1:0),e.uniform1i(a.fMask,tt),e.useProgram(a.blit),e.uniform2f(a.bRes,c.W,c.H),e.uniform2f(a.bGrid,c.cols,c.rows),e.uniform2f(a.bCell,c.cw,c.ch),e.uniform1i(a.bField,Je),e.uniform1i(a.bAtlas,et),e.uniform1f(a.bLevels,E),e.uniform1f(a.bVariants,r.variants),e.uniform1f(a.bExtraBase,E*r.variants),e.uniform1f(a.bCut,r.cutoff),e.uniform1f(a.bDither,r.dither),e.bindVertexArray(a.vao),e.disable(e.BLEND),ct(),ce(ye)}function ce(i){let f=h;if(!f||P)return;ye=i,e.bindFramebuffer(e.FRAMEBUFFER,S),e.viewport(0,0,f.cols,f.rows),e.useProgram(a.field),e.uniform1f(a.fT,i),e.drawArrays(e.TRIANGLES,0,3),e.bindFramebuffer(e.FRAMEBUFFER,null),e.viewport(0,0,f.W,f.H),e.useProgram(a.blit),e.drawArrays(e.TRIANGLES,0,3)}function Le(i){if(F=0,!Y)return;if(F=requestAnimationFrame(Le),!ze(i,ee,v))return;ee=i;let f=z?performance.now():0;if(Ee+=Math.min((i-te)/1000,0.25),te=i,ce(Ee),z){let m=ie%J;re[m]=performance.now()-f,oe[m]=f,ie++}}function lt(){if(Y)return;Y=!0,te=ee=performance.now(),F=requestAnimationFrame(Le)}function X(){if(Y=!1,F)cancelAnimationFrame(F);F=0}function A(){if(G||P||!Me)return;if(R()||K!==null)X(),ce(K??u);else if(W&&Re&&!document.hidden)lt();else X()}function q(){clearTimeout(ne),ne=window.setTimeout(()=>{if(G)return;se(),A()},Ft)}let Ae=()=>A(),Se=()=>A(),Te=(i)=>{i.preventDefault(),P=!0,X(),n.onStatus?.("lost")},_e=()=>{P=!1,O=V=H=null,S=null;try{a=je(e,s),se(),A(),n.onStatus?.("ready")}catch(i){console.warn("ascii backdrop could not recover:",i),P=!0,n.onStatus?.("lost")}},Pe=typeof ResizeObserver==="function"?new ResizeObserver(()=>{if(t.clientWidth!==D||t.clientHeight!==xe)q()}):null,we=typeof IntersectionObserver==="function"?new IntersectionObserver((i)=>{let f=i[i.length-1];if(f)Re=f.isIntersecting;A()}):null;Pe?.observe(t),we?.observe(t),g?.addEventListener("change",Se),document.addEventListener("visibilitychange",Ae),t.addEventListener("webglcontextlost",Te),t.addEventListener("webglcontextrestored",_e);async function ut(){if(document.fonts){let i=0;try{await Promise.race([document.fonts.load(`400 16px ${d}`,r.candidates+r.extras),new Promise((f)=>{i=window.setTimeout(f,Gt)})])}catch{}clearTimeout(i)}if(G)return;if(Me=!0,se(),A(),h)n.onStatus?.("ready")}let ft=ut().catch((i)=>{console.warn("ascii backdrop failed to start:",i),X(),n.onStatus?.("lost")});return{start(){W=!0,K=null,A()},stop(){W=!1,A()},resize:q,renderStatic(i){W=!1,K=i,A()},destroy(){if(G)return;if(G=!0,X(),clearTimeout(ne),Pe?.disconnect(),we?.disconnect(),g?.removeEventListener("change",Se),U?.removeEventListener("change",q),document.removeEventListener("visibilitychange",Ae),t.removeEventListener("webglcontextlost",Te),t.removeEventListener("webglcontextrestored",_e),!P){for(let i of[O,V,H])if(i)e.deleteTexture(i);if(S)e.deleteFramebuffer(S);e.deleteProgram(a.field),e.deleteProgram(a.blit),e.deleteVertexArray(a.vao),e.getExtension("WEBGL_lose_context")?.loseContext()}},ready:ft,stats(){let i=Math.min(ie,J);if(!z||i<3||!h)return null;let f=Array.from(re.subarray(0,i)).sort((p,E)=>p-E),m=Number.POSITIVE_INFINITY,y=0,c=0;for(let p=0;p<i;p++)m=Math.min(m,oe[p]??0),y=Math.max(y,oe[p]??0),c+=re[p]??0;return{frames:i,fps:(i-1)/((y-m)/1000),meanMs:c/i,p95Ms:f[Math.floor(i*0.95)]??0,maxMs:f[i-1]??0,grid:`${h.cols}x${h.rows}`,cell:`${h.cw}x${h.ch}@${h.dpr}`}}}}var be={dim:[58,64,76],ink:[180,187,200],accent:[109,107,255],accentInk:[154,164,255],star:[238,240,255]};var rt=`
const float SRHO[4] = float[4](1.5, 1.9, 1.22, 2.4);
const float SW[4]   = float[4](0.21, -0.14, 0.31, 0.09);
const float SP[4]   = float[4](0.4, 2.2, 4.1, 5.3);

vec2 eclipse(ivec2 cell, vec2 px, float t) {
	vec3 L = normalize(vec3(0.66, -0.42, -0.62));
	float R = uOrb.z;
	vec2 c0 = uOrb.xy;
	vec2 uv = (px - c0) / R; float u = uv.x, v = uv.y;
	float a = 0.27 + 0.04 * sin(t * 0.07), b = -0.36 + 0.05 * sin(t * 0.045 + 1.0);
	vec3 Nr = vec3(-cos(a) * sin(b), cos(a) * cos(b), sin(a));
	float e1l = length(Nr.xy);
	vec3 e1 = vec3(Nr.y / e1l, -Nr.x / e1l, 0.0);
	vec3 e2 = cross(Nr, e1);
	float spin = t * 0.11, cs = cos(spin), sn = sin(spin);
	float tl = 0.42, ct = cos(tl), st = sin(tl);
	float r2 = u * u + v * v;
	float l = 0.0, h = 0.0;

	// faint dust
	float hh = hash2(cell);
	if (hh > 0.993) l = 0.08 + 0.08 * (0.5 + 0.5 * sin(t * 0.7 + hh * 977.0));

	// ring
	float ringL = 0.0, ringZ = 1e9, ringH = 0.0;
	float zp = -(Nr.x * u + Nr.y * v) / Nr.z;
	float rho = sqrt(r2 + zp * zp);
	if (rho > 1.34 && rho < 2.2) {
		float pxp = u * e1.x + v * e1.y, pyp = u * e2.x + v * e2.y + zp * e2.z;
		float phi = atan(pyp, pxp) - t * 0.18 / pow(rho, 1.5);
		float dens = 0.55 + 0.45 * sin(rho * 31.0 + 1.7 * sin(rho * 7.0));
		dens *= 0.7 + 0.3 * sin(phi * 9.0 + rho * 5.0);
		if (rho > 1.7 && rho < 1.8) dens *= 0.1;
		if (rho > 2.06) dens *= (2.2 - rho) / 0.14;
		vec3 P = vec3(u, v, zp);
		float pl = dot(P, L); vec3 dd = P - pl * L;
		float shadow = (pl > 0.0 && dot(dd, dd) < 1.0) ? 0.1 : 1.0;
		ringL = dens * 1.35 * shadow * (0.55 + 0.45 * abs(dot(Nr, L)) * 1.6);
		ringZ = zp;
		if (rho < 1.45 && shadow > 0.5) ringH = 1.0;
	}

	// sphere
	float sphL = 0.0, sphZ = 1e9, sphH = 0.0;
	if (r2 < 1.0) {
		float z = -sqrt(1.0 - r2);
		float x1 = u * cs + z * sn, z1 = -u * sn + z * cs;
		float y2 = v * ct - z1 * st, z2 = v * st + z1 * ct;
		float s = y2 * 7.0 + 1.7 * sin(x1 * 2.6 + z2 * 1.9 + t * 0.25) + 0.9 * sin(x1 * 5.1 - z2 * 4.3 - t * 0.15 + y2 * 3.0);
		float bands = 0.5 + 0.5 * sin(s);
		float diff = max(0.0, dot(vec3(u, v, z), L));
		float rim = pow(1.0 - abs(z), 3.0) * (0.2 + 0.8 * max(0.0, diff + 0.25));
		sphL = 0.06 + pow(diff, 0.95) * (0.34 + 0.66 * bands) + rim * 0.7;
		sphZ = z;
		if (rim > 0.28) sphH = 1.0;
	} else if (r2 < 1.26) {
		float r = sqrt(r2), f = 1.0 - (r - 1.0) / 0.12;
		float lit = max(0.0, dot(vec2(u, v) / r, L.xy));
		float hl = f * f * (0.15 + 0.5 * lit);
		if (hl > l) { l = hl; h = 1.0; }
	}
	if (sphL > 0.0 && ringL > 0.0) {
		if (ringZ < sphZ) { if (ringL > l) { l = ringL; h = ringH; } } else { l = sphL; h = sphH; }
	} else if (sphL > 0.0) { l = sphL; h = sphH; }
	else if (ringL > l) { l = ringL; h = ringH; }

	// four stars orbiting in the ring plane
	for (int k = 0; k < 4; k++) {
		float ph = SP[k] + t * SW[k];
		vec3 S = SRHO[k] * (cos(ph) * e1 + sin(ph) * e2);
		if (S.x * S.x + S.y * S.y < 1.0 && S.z > 0.0) continue;
		ivec2 d = cell - ivec2(floor((c0 + S.xy * R) / uCell));
		if (abs(d.x) <= 2 && abs(d.y) <= 1) {
			float dist = length(vec2(float(d.x) * 0.55, float(d.y)));
			float val = dist < 0.5 ? 1.0 : max(0.0, 0.5 - dist * 0.25);
			if (val > l) { l = val; h = 2.0; }
		}
	}
	return vec2(l, h);
}

vec4 field(ivec2 cell, vec2 px, float t) {
	vec2 r = eclipse(cell, px, t);
	return vec4(r, 0.0, 1.0);
}`;function he(t,n){return nt(t,{...n,scene:"eclipse"},{eclipse:rt})}function ot(){let t=document.querySelector("canvas[data-ascii-canvas]");if(!t)return;let n=document.documentElement,o=document.querySelector("[data-ascii-card]"),r=he(t,{palette:be,fontFamily:t.dataset.font??pe,maskRect:()=>o?.getBoundingClientRect()??null,quietRects:()=>Array.from(document.querySelectorAll("[data-ascii-quiet]"),(s)=>s.getBoundingClientRect()),onStatus:(s)=>{n.dataset.ascii=s==="ready"?"on":"off"}});if(!r){n.dataset.ascii="off";return}if(r.start(),o&&typeof ResizeObserver==="function")new ResizeObserver(()=>r.resize()).observe(o)}if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",ot,{once:!0});else ot();Object.assign(window,{NebulaAscii:{createAscii:he,PALETTE:be}});})();
