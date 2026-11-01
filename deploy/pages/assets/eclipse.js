(()=>{var fe={dim:[58,64,76],ink:[180,187,200],accent:[109,107,255],accentInk:[154,164,255],star:[238,240,255]};function me(t,n){return Math.min(t>0?t:1,n)}var N={min:0.8,feather:40,pad:0},De={min:0.07,feather:70,pad:30};function H(t){return Math.min(1,Math.max(0.4,t/1100))}function q(t,n,r,o=1){return{x0:t.left*n,y0:t.top*n,x1:t.right*n,y1:t.bottom*n,min:r.min,feather:r.feather*n*o,pad:r.pad*n*o}}function de(t,n,r){let o=1;for(let e=0;e<r.length;e++){let i=r[e],p=Math.max(i.x0-t,0,t-i.x1),l=Math.max(i.y0-n,0,n-i.y1),s=Math.hypot(p,l)-i.pad;if(s<i.feather){let m=Math.max(s,0)/i.feather;o=Math.min(o,i.min+(1-i.min)*(m*m*(3-2*m)))}}return o}var be=720,ot=0.45;function Oe(t,n,r,o,e=be){let i={cx:t*0.77,cy:n*0.45,r:Math.min(t,n)*0.37,dim:1};if(!(t<n*1.05||t/r<e)||!o)return i;return{cx:t/2,cy:(o.top+o.bottom)/2,r:Math.min(t*0.46,n*0.3),dim:ot}}function pe(t,n,r){return t-n>=r-1.5}var he=(t,n,r)=>[t[0]+(n[0]-t[0])*r,t[1]+(n[1]-t[1])*r,t[2]+(n[2]-t[2])*r];var it="eclipse",at=24,ct=".'`\":;,_-~^+=*<>|/\\()[]{}#";var D={min:0.05,feather:90,pad:24},xe={eclipse:{name:"eclipse",fps:30,candidates:"",levels:1,variants:1,extras:"",extraTone:0,gamma:1,cutoff:0,dither:0,maskMode:"level",panel:N,quiet:De,staticTime:14},"dial-out":{name:"dial-out",fps:20,candidates:".':-=+*",levels:8,variants:1,extras:"",extraTone:0.45,gamma:0.55,cutoff:0.04,dither:0.5,maskMode:"alpha",panel:N,quiet:D,staticTime:32},"lost-signal":{name:"lost-signal",fps:12,candidates:ct,levels:7,variants:3,extras:"",extraTone:0.45,gamma:0.8,cutoff:0.04,dither:0.4,maskMode:"alpha",panel:N,quiet:D,staticTime:9},dome:{name:"dome",fps:5,candidates:".':+*",levels:6,variants:1,extras:"_",extraTone:0.5,gamma:0.5,cutoff:0.05,dither:0.3,maskMode:"alpha",panel:N,quiet:D,staticTime:40},band:{name:"band",fps:10,candidates:".,':+*",levels:7,variants:1,extras:".",extraTone:0.95,gamma:0.5,cutoff:0.04,dither:0.3,maskMode:"alpha",panel:N,quiet:D,staticTime:20}};function Ee(t){return t!==void 0&&Object.hasOwn(xe,t)?xe[t]:xe[it]}function Re(t,n){let r=n??t.fps,o=Math.max(at,t.fps);return Math.min(o,Math.max(1,Number.isFinite(r)?r:t.fps))}var st=0.09;var lt=0.46,ut=0.44,ft=0.12,mt=0.16,_=(t,n,r)=>Math.min(r,Math.max(n,t));function Be(t,n){return(D.pad+D.feather)*n*H(t/n)}function dt(t,n){let r=t?[t,...n]:n;if(r.length===0)return null;return{left:Math.min(...r.map((o)=>o.left)),right:Math.max(...r.map((o)=>o.right))}}var bt=0.4,pt=0.3;function Ue(t,n){return 0.15*n+0.025*t}var ht=1,xt=0.8;function Et(t,n,r,o){let e=Ue(t,n),i=ht*n,p=Be(t,r),l=xt*t;for(let s of o){if(s.bottom<0||s.top>n)continue;let m=Math.min(s.bottom,n);l=Math.max(l,s.right+p+i*(m/n))}return l=Math.min(l,t+0.5*e),{top:l,bottom:l-i}}function ge(t,{W:n,H:r,dpr:o,rect:e,quiet:i=[],narrowBelow:p=be,focus:l}){let s={cx:0,cy:0,r:0,dim:1},m=dt(e,i),R=Be(n,o);switch(t){case"eclipse":return{orb:Oe(n,r,o,e,p),focus:[0,0,0,0]};case"dial-out":{let b=n/o<p,u=0.74;if(!b&&m){let x=112*o;u=(m.left+m.right)/2<n/2?Math.max(0.74,(m.right+R+x)/n):Math.min(0.26,(m.left-R-x)/n)}let h=l??{x:u,y:bt};return{orb:s,focus:[_(h.x,0.08,0.92)*n,_(h.y,0.08,0.8)*r,0,0]}}case"dome":{let b=0.76;if(m&&(m.left+m.right)/2<n/2)b=Math.max(b,(m.right+R)/n);let u=l??{x:b,y:pt};return{orb:s,focus:[1.02*n,-0.02*r,_(u.x,0.1,0.94)*n,_(u.y,0.1,0.8)*r]}}case"band":{let b=Et(n,r,o,e?[e,...i]:i);return{orb:s,focus:[b.top,b.bottom,Ue(n,r),0]}}case"lost-signal":{let b=st*Math.min(n,r),u=n/2,h=r/2,x=n*0.28,E=r*0.3;if(e)u=(e.left+e.right)/2,h=(e.top+e.bottom)/2,x=(e.right-e.left)/2+b+n*0.03,E=(e.bottom-e.top)/2+b+r*0.05;return x=_(x,n*ft,n*lt),E=_(E,r*mt,r*ut),u=_(u,x,Math.max(x,n-x)),h=_(h,E,Math.max(E,r-E)),{orb:s,focus:[u,h,x,E]}}default:return{orb:s,focus:[0,0,0,0]}}}var Y=`#version 300 es
void main(){ vec2 p = vec2(float((gl_VertexID<<1)&2), float(gl_VertexID&2)); gl_Position = vec4(p*2.0-1.0, 0.0, 1.0); }`;function We(t,n,r){let o=t.createShader(n);if(!o)throw Error("createShader failed");if(t.shaderSource(o,r),t.compileShader(o),!t.getShaderParameter(o,t.COMPILE_STATUS))throw Error(t.getShaderInfoLog(o)??"shader compile failed");return o}function ve(t,n,r){let o=t.createProgram();if(!o)throw Error("createProgram failed");let e=We(t,t.VERTEX_SHADER,n),i=We(t,t.FRAGMENT_SHADER,r);if(t.attachShader(o,e),t.attachShader(o,i),t.linkProgram(o),t.deleteShader(e),t.deleteShader(i),!t.getProgramParameter(o,t.LINK_STATUS))throw Error(t.getProgramInfoLog(o)??"program link failed");return o}var Xe=`#version 300 es
precision highp float; precision highp int;
/* Canvas, device px. */
uniform vec2 uRes;
/* Cell size, device px. */
uniform float uCell;
/* Mask grid: cols, rows. */
uniform vec2 uGrid;
uniform float uT;
/* Orb centre (xy, device px, y down) and radius (z). */
uniform vec3 uOrb;
/* Field strength over the whole canvas (lower when the orb sits behind the card). */
uniform float uDim;
/* Eased cursor, as fractions of the canvas with y up, and how much it counts (0 = default light). */
uniform vec2 uCur;
uniform float uCurW;
uniform sampler2D uMask;
out vec4 o;

float h(vec2 p){ return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453); }
float n(vec2 p){
	vec2 i = floor(p), f = fract(p);
	f = f * f * (3.0 - 2.0 * f);
	return mix(mix(h(i), h(i + vec2(1.0, 0.0)), f.x), mix(h(i + vec2(0.0, 1.0)), h(i + 1.0), f.x), f.y);
}
float fbm(vec2 p){
	float s = 0.0, a = 0.5;
	for (int i = 0; i < 5; i++) { s += a * n(p); p = p * 2.03 + vec2(17.1, 3.7); a *= 0.5; }
	return s;
}
float b2(vec2 a){ a = floor(a); return fract(dot(a, vec2(0.5, a.y * 0.75))); }
float b4(vec2 a){ return b2(0.5 * a) * 0.25 + b2(a); }
float b8(vec2 a){ return b4(0.5 * a) * 0.25 + b2(a); }

void main(){
	vec2 cell = vec2(gl_FragCoord.x - 0.5, uGrid.y - 0.5 - gl_FragCoord.y);
	vec2 m = texelFetch(uMask, ivec2(cell), 0).rg;
	if (m.g < 0.004) { o = vec4(0.0); return; }

	vec2 pu = vec2((cell.x + 0.5) * uCell, uRes.y - (cell.y + 0.5) * uCell) / uRes.y;
	vec2 mm = uCur * uRes / uRes.y;
	vec2 oc = vec2(uOrb.x, uRes.y - uOrb.y) / uRes.y;
	float rad = uOrb.z / uRes.y;
	vec2 d = (pu - oc) / rad;
	float r = length(d);

	float hz = 0.0;
	if (r >= 1.0) {
		float w = fbm(pu * 1.6 + vec2(uT * 0.02, 0.0));
		hz = smoothstep(0.42, 0.95, fbm(pu * 2.2 + w * 1.6 - vec2(0.0, uT * 0.015)));
		hz *= mix(0.35, 1.0, 1.0 - smoothstep(0.7, 2.5, r));
	}
	float f = hz * 0.55;

	float ca = cos(-0.38), sa = sin(-0.38);
	vec2 dr = vec2(ca * d.x - sa * d.y, sa * d.x + ca * d.y);
	float rr = length(vec2(dr.x, dr.y * 3.4));
	vec2 lp = mix(vec2(-0.55, 0.6), (mm - oc) / rad * 0.75, uCurW);
	vec3 L = normalize(vec3(lp, 0.75));
	/* Gaussian bands, so the edges dither out: inner (centre 1.6 radii, only its core reaches tone 2), outer (1.95, mostly tone 1 with sparse tone 2), a dark gap between, and a faint dust band out to ~2.15. */
	float ring = exp(-pow((rr - 1.6) * 11.0, 2.0)) * 0.95 + exp(-pow((rr - 1.95) * 13.0, 2.0)) * 0.55 + exp(-pow((rr - 2.07) * 9.0, 2.0)) * 0.18;
	if (ring > 0.01) {
		/* The ring plane's point under this cell, and the planet's shadow (a cylinder along the light) on it. */
		vec3 P = vec3(d, -3.25 * dr.y);
		float pl = dot(P, L);
		if (pl < 0.0) ring *= mix(0.12, 1.0, smoothstep(0.9, 1.1, length(P - pl * L)));
	}

	if (r < 1.0) {
		vec3 nn = vec3(d, sqrt(max(0.0, 1.0 - r * r)));
		float lam = max(dot(nn, L), 0.0);
		float lon = atan(nn.x, nn.z) + uT * 0.12;
		f = 0.04 + lam * (0.5 + 0.75 * fbm(vec2(lon * 1.4, nn.y * 5.5)));
		if (dr.y < 0.0) f = max(f, ring);
	} else {
		f = max(f, ring);
		f += exp(-(r - 1.0) * 4.76) * 0.22;
	}
	f += uCurW * 0.38 * exp(-dot(pu - mm, pu - mm) * 16.0) * (0.45 + hz);
	f *= smoothstep(0.0, 0.14, pu.y) * m.r * m.g * uDim;

	float lv = floor(clamp(f, 0.0, 1.0) * 2.0 + b8(cell));
	if (r >= 1.0 && h(cell) > 0.9965 && m.r * m.g > 0.5) {
		float s = 0.55 + 0.45 * sin(uT * 1.6 + h(cell + 3.0) * 40.0);
		lv = max(lv, s > 0.2 ? 2.0 : 1.0);
	}
	o = vec4(lv * 0.5, 0.0, 0.0, 1.0);
}`,He=`#version 300 es
precision highp float; precision highp int;
uniform vec2 uRes;
uniform float uCell;
uniform vec2 uGrid;
uniform sampler2D uField;
uniform vec3 uC1;
uniform vec3 uC2;
out vec4 o;
void main(){
	vec2 p = vec2(gl_FragCoord.x, uRes.y - gl_FragCoord.y);
	ivec2 c = ivec2(clamp(floor(p / uCell), vec2(0.0), uGrid - 1.0));
	int tone = int(texelFetch(uField, ivec2(c.x, int(uGrid.y) - 1 - c.y), 0).r * 2.0 + 0.5);
	o = tone < 1 ? vec4(0.0) : vec4(tone < 2 ? uC1 : uC2, 1.0);
}`;var Qe=2,qe=0.08,Rt=0.06,O=[0.7,0.6],gt=Qe,vt=120,K=900;function Tt(t){let n=(r)=>[r[0]/255,r[1]/255,r[2]/255];return{c1:n(he(t.accent,[0,0,0],0.55)),c2:n(t.accentInk)}}function yt(t,n,r){let o=Math.max(1,Math.round(t*r)),e=Math.max(1,Math.round(n*r)),i=Qe*r;return{W:o,H:e,dpr:r,cell:i,cols:Math.ceil(o/i),rows:Math.ceil(e/i)}}function Mt(t,n,r,o){let{cols:e,rows:i,cell:p}=t,l=0;for(let s=0;s<i;s++){let m=(s+0.5)*p;for(let R=0;R<e;R++){let b=(R+0.5)*p;o[l++]=Math.round(de(b,m,n)*255),o[l++]=Math.round(de(b,m,r)*255)}}return o}var Lt=2.07,At=3.4,Ve=0.38,_t=0.08,wt=0.12;function St(){let t=Lt+_t,n=t/At,r=Math.cos(Ve),o=Math.sin(Ve);return{halfW:Math.hypot(t*r,n*o),halfH:Math.max(1+wt,Math.hypot(t*o,n*r))}}var Q=0.01,Ft=0.06,ze=0.12,Gt=0.4,Pt=0.26,Ct=0.16,kt=0.52,It=0.34,Nt=0.9,Dt=0.04,Te=0.08,Ot=0.6;function Bt(t){let{W:n,H:r,rect:o,quiet:e,narrow:i}=t,{halfW:p,halfH:l}=St(),s=o?[o,...e]:e;if(i){let h=It*n,E=Math.max(0,...s.map((C)=>C.bottom))+Nt*h;if(E-h<r)return{cx:n/2,cy:E,r:h,dim:1};let y=Math.min(n*(1-2*Dt)/(2*p),r*(1-2*Te)/(2*l)),g=r*Te+l*y,G=Math.max(g,r*(1-Te)-l*y),P=o?(o.top+o.bottom)/2:r/2;return{cx:n/2,cy:Math.min(Math.max(P,g),G),r:y,dim:Ot}}let m=(h)=>Math.min(h/(2*p),r*(1-2*Ft)/(2*l)),R=n*Gt,b=n*(1-Q);if(s.length>0){let h=Math.min(...s.map((g)=>g.left)),x=Math.max(...s.map((g)=>g.right)),E=n*(1-Q)-x,y=h-n*Q;if(E>=y)R=x-ze*n;else R=n*Q,b=h+ze*n}let u=Math.max(Math.min(m(b-R),Pt*r),Ct*r);return{cx:(R+b)/2,cy:r*kt,r:u,dim:1}}function ye(t,n,r,o){return t+(n-t)*(1-(1-r)**(o*60))}function Ut(t,n,r,o){if(t.width<=0||t.height<=0)return!1;let e=(n-t.left)/t.width,i=1-(r-t.top)/t.height;return o[0]=e,o[1]=i,e>=0&&e<=1&&i>=0&&i<=1}var Ye=0,$e=1;function Ke(t){let n=ve(t,Y,Xe),r=ve(t,Y,He),o=(i)=>t.getUniformLocation(n,i),e=(i)=>t.getUniformLocation(r,i);return{field:n,blit:r,vao:t.createVertexArray(),fRes:o("uRes"),fCell:o("uCell"),fGrid:o("uGrid"),fT:o("uT"),fOrb:o("uOrb"),fDim:o("uDim"),fCur:o("uCur"),fCurW:o("uCurW"),fMask:o("uMask"),bRes:e("uRes"),bCell:e("uCell"),bGrid:e("uGrid"),bField:e("uField"),bC1:e("uC1"),bC2:e("uC2")}}function j(t,n){let r=Ee("eclipse"),o=t.getContext("webgl2",{alpha:!0,premultipliedAlpha:!0,antialias:!1,depth:!1,stencil:!1,powerPreference:"low-power",preserveDrawingBuffer:!1});if(!o)return null;let e=o,i;try{i=Ke(e)}catch(a){return console.warn("ascii backdrop disabled:",a),null}let p=Tt(n.palette),l=n.staticTime??r.staticTime,s=n.dprCap??2,m=1000/Re(r,n.fps),R=n.reducedMotion===void 0&&typeof matchMedia==="function"?matchMedia("(prefers-reduced-motion: reduce)"):null,b=()=>n.reducedMotion??R?.matches??!1,u=null,h=0,x=0,E=null,y=null,g=null,G=new Uint8Array(0),P=!1,C=null,V=!1,k=0,Z=0,J=0,Me=l,Le=l,Ae=!0,w=!1,I=!1,_e=!1,ee=0,B=null,te=O[0],ne=O[1],re=0,oe=0,ie=[O[0],O[1]],U=n.stats===!0,ae=new Float64Array(U?K:0),ce=new Float64Array(U?K:0),se=0;function Ze(a,d,f){let c=t.getBoundingClientRect(),L=(T)=>({left:T.left-c.left,top:T.top-c.top,right:T.right-c.left,bottom:T.bottom-c.top}),v=H(t.clientWidth),A={min:0,feather:gt,pad:0},S=[],z=[];if(d)S.push(q(L(d),a.dpr,r.panel,v));for(let T of f)S.push(q(L(T),a.dpr,r.quiet,v)),z.push(q(L(T),a.dpr,A));return{soft:S,hard:z}}function we(a){let d=e.createTexture();return e.activeTexture(e.TEXTURE0+a),e.bindTexture(e.TEXTURE_2D,d),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_MIN_FILTER,e.NEAREST),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_MAG_FILTER,e.NEAREST),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_WRAP_S,e.CLAMP_TO_EDGE),e.texParameteri(e.TEXTURE_2D,e.TEXTURE_WRAP_T,e.CLAMP_TO_EDGE),d}function le(){if(I||w)return;let{clientWidth:a,clientHeight:d}=t;if(a===0||d===0){u=null;return}let f=me(window.devicePixelRatio,s),c=yt(a,d,f);u=c,h=a,x=d,t.width=c.W,t.height=c.H;let L=t.getBoundingClientRect(),v=(F)=>({left:(F.left-L.left)*f,top:(F.top-L.top)*f,right:(F.right-L.left)*f,bottom:(F.bottom-L.top)*f}),A=n.maskRect?.()??null,S=n.quietRects?.()??[],z=ge("eclipse",{W:c.W,H:c.H,dpr:f,rect:A?v(A):null,quiet:S.map(v),narrowBelow:n.narrowBelow}).orb,T=Bt({W:c.W,H:c.H,rect:A?v(A):null,quiet:S.map(v),narrow:z.dim<1}),{soft:nt,hard:rt}=Ze(c,A,S),Ne=c.cols*c.rows*2;if(G.length!==Ne)G=new Uint8Array(Ne);Mt(c,nt,rt,G);for(let F of[E,y])if(F)e.deleteTexture(F);if(g)e.deleteFramebuffer(g);e.pixelStorei(e.UNPACK_ALIGNMENT,1),E=we(Ye),e.texImage2D(e.TEXTURE_2D,0,e.RG8,c.cols,c.rows,0,e.RG,e.UNSIGNED_BYTE,G),y=we($e),e.texImage2D(e.TEXTURE_2D,0,e.R8,c.cols,c.rows,0,e.RED,e.UNSIGNED_BYTE,null),g=e.createFramebuffer(),e.bindFramebuffer(e.FRAMEBUFFER,g),e.framebufferTexture2D(e.FRAMEBUFFER,e.COLOR_ATTACHMENT0,e.TEXTURE_2D,y,0),e.bindFramebuffer(e.FRAMEBUFFER,null),e.useProgram(i.field),e.uniform2f(i.fRes,c.W,c.H),e.uniform1f(i.fCell,c.cell),e.uniform2f(i.fGrid,c.cols,c.rows),e.uniform3f(i.fOrb,T.cx,T.cy,T.r),e.uniform1f(i.fDim,T.dim),e.uniform1i(i.fMask,Ye),e.useProgram(i.blit),e.uniform2f(i.bRes,c.W,c.H),e.uniform1f(i.bCell,c.cell),e.uniform2f(i.bGrid,c.cols,c.rows),e.uniform1i(i.bField,$e),e.uniform3f(i.bC1,p.c1[0],p.c1[1],p.c1[2]),e.uniform3f(i.bC2,p.c2[0],p.c2[1],p.c2[2]),e.bindVertexArray(i.vao),e.disable(e.BLEND),Je(),ue(Le,!1)}function Je(){if(B?.removeEventListener("change",X),B=null,typeof matchMedia!=="function")return;B=matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`),B.addEventListener("change",X,{once:!0})}function ue(a,d){let f=u;if(!f||w)return;Le=a,e.bindFramebuffer(e.FRAMEBUFFER,g),e.viewport(0,0,f.cols,f.rows),e.useProgram(i.field),e.uniform1f(i.fT,a),e.uniform2f(i.fCur,d?te:O[0],d?ne:O[1]),e.uniform1f(i.fCurW,d?re:0),e.drawArrays(e.TRIANGLES,0,3),e.bindFramebuffer(e.FRAMEBUFFER,null),e.viewport(0,0,f.W,f.H),e.useProgram(i.blit),e.drawArrays(e.TRIANGLES,0,3)}function Se(a){if(k=0,!V)return;if(k=requestAnimationFrame(Se),!pe(a,Z,m))return;Z=a;let d=U?performance.now():0,f=Math.min((a-J)/1000,0.25);if(Me+=f,J=a,te=ye(te,ie[0],qe,f),ne=ye(ne,ie[1],qe,f),re=ye(re,oe,Rt,f),ue(Me,!0),U){let c=se%K;ae[c]=performance.now()-d,ce[c]=d,se++}}function et(){if(V)return;V=!0,J=Z=performance.now(),k=requestAnimationFrame(Se)}function W(){if(V=!1,k)cancelAnimationFrame(k);k=0}function M(){if(I||w||!_e)return;if(b()||C!==null)W(),ue(C??l,!1);else if(P&&Ae&&!document.hidden)et();else W()}function X(){clearTimeout(ee),ee=window.setTimeout(()=>{if(I)return;le(),M()},vt)}let Fe=(a)=>{if(b())return;oe=Ut(t.getBoundingClientRect(),a.clientX,a.clientY,ie)?1:0},Ge=()=>{oe=0},Pe=(a)=>{a.preventDefault(),w=!0,W(),n.onStatus?.("lost")},Ce=()=>{w=!1,E=y=null,g=null;try{i=Ke(e),le(),M(),n.onStatus?.("ready")}catch(a){console.warn("ascii backdrop could not recover:",a),w=!0,n.onStatus?.("lost")}},ke=typeof ResizeObserver==="function"?new ResizeObserver(()=>{if(t.clientWidth!==h||t.clientHeight!==x)X()}):null,Ie=typeof IntersectionObserver==="function"?new IntersectionObserver((a)=>{let d=a[a.length-1];if(d)Ae=d.isIntersecting;M()}):null;ke?.observe(t),Ie?.observe(t),R?.addEventListener("change",M),document.addEventListener("visibilitychange",M),document.documentElement.addEventListener("pointerleave",Ge,{passive:!0}),window.addEventListener("pointermove",Fe,{passive:!0}),t.addEventListener("webglcontextlost",Pe),t.addEventListener("webglcontextrestored",Ce);let tt=Promise.resolve().then(()=>{if(I)return;if(_e=!0,le(),M(),u)n.onStatus?.("ready")}).catch((a)=>{console.warn("ascii backdrop failed to start:",a),W(),n.onStatus?.("lost")});return{start(){P=!0,C=null,M()},stop(){P=!1,M()},resize:X,renderStatic(a){P=!1,C=a,M()},destroy(){if(I)return;if(I=!0,W(),clearTimeout(ee),ke?.disconnect(),Ie?.disconnect(),R?.removeEventListener("change",M),B?.removeEventListener("change",X),document.removeEventListener("visibilitychange",M),document.documentElement.removeEventListener("pointerleave",Ge),window.removeEventListener("pointermove",Fe),t.removeEventListener("webglcontextlost",Pe),t.removeEventListener("webglcontextrestored",Ce),!w){for(let a of[E,y])if(a)e.deleteTexture(a);if(g)e.deleteFramebuffer(g);e.deleteProgram(i.field),e.deleteProgram(i.blit),e.deleteVertexArray(i.vao),e.getExtension("WEBGL_lose_context")?.loseContext()}},ready:tt,stats(){let a=Math.min(se,K);if(!U||a<3||!u)return null;let d=Array.from(ae.subarray(0,a)).sort((v,A)=>v-A),f=Number.POSITIVE_INFINITY,c=0,L=0;for(let v=0;v<a;v++)f=Math.min(f,ce[v]??0),c=Math.max(c,ce[v]??0),L+=ae[v]??0;return{frames:a,fps:(a-1)/((c-f)/1000),meanMs:L/a,p95Ms:d[Math.floor(a*0.95)]??0,maxMs:d[a-1]??0,grid:`${u.cols}x${u.rows}`,cell:`${u.cell}x${u.cell}@${u.dpr}`}}}}function je(){let t=document.querySelector("canvas[data-ascii-canvas]");if(!t)return;let n=document.documentElement,r=document.querySelector("[data-ascii-card]"),o=j(t,{palette:fe,maskRect:()=>r?.getBoundingClientRect()??null,quietRects:()=>Array.from(document.querySelectorAll("[data-ascii-quiet]"),(e)=>e.getBoundingClientRect()),onStatus:(e)=>{n.dataset.ascii=e==="ready"?"on":"off"}});if(!o){n.dataset.ascii="off";return}if(o.start(),r&&typeof ResizeObserver==="function")new ResizeObserver(()=>o.resize()).observe(r)}if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",je,{once:!0});else je();Object.assign(window,{NebulaAscii:{createAscii:j,PALETTE:fe}});})();
