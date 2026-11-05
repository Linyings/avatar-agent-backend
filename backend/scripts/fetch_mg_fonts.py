"""Bundle upstream-requested open fonts. No render-time network access."""
from pathlib import Path
import concurrent.futures,hashlib,json,urllib.request

root=Path(__file__).resolve().parents[1]/'avatar-runtime/mg-upstream'
# Fonts required by OpenType geometry parsing cannot use browser fallbacks.
fonts=[
 ('lxgwmarkergothic/LXGWMarkerGothic-Regular.ttf','assets/fonts/cjk/lxgwmarkergothic/LXGWMarkerGothic-Regular.ttf'),
 ('unbounded/Unbounded[wght].ttf','demos/03-isometric/assets/fonts/Unbounded-800-static.ttf'),
 ('mrdafoe/MrDafoe-Regular.ttf','demos/10-synthwave/assets/fonts/MrDafoe-Regular.ttf'),
 ('fraunces/Fraunces[SOFT,WONK,opsz,wght].ttf','demos/07-liquid/assets/Fraunces-Liquid-72-900-Soft.ttf'),
 ('outfit/Outfit[wght].ttf','demos/08-morph/assets/fonts/Outfit-SemiBold.ttf'),
]
def fetch(item):
    relative,dest=item;url='https://raw.githubusercontent.com/google/fonts/main/ofl/'+urllib.parse.quote(relative,safe='/')
    path=root/dest;path.parent.mkdir(parents=True,exist_ok=True)
    if not path.exists():
        data=urllib.request.urlopen(url,timeout=90).read();path.write_bytes(data)
    license_url='https://raw.githubusercontent.com/google/fonts/main/ofl/'+relative.split('/')[0]+'/OFL.txt'
    lic=path.parent/(relative.split('/')[0]+'-OFL.txt')
    if not lic.exists():lic.write_bytes(urllib.request.urlopen(license_url,timeout=60).read())
    return {'source':url,'path':dest,'sha256':hashlib.sha256(path.read_bytes()).hexdigest(),'license':'SIL OFL 1.1','license_path':lic.relative_to(root).as_posix(),'note':'Variable font files retained where static cuts were excluded upstream; browser typography may differ.'}
with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:rows=list(pool.map(fetch,fonts))
(root/'FONT-SOURCES.json').write_text(json.dumps(rows,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps({'bundled_open_fonts':len(rows)}))
