#timeout 950
#pull /home/chuanming/golive-telephony/codex-latency/prequote-hit-gap-release-f842928-20261031-2/summary.json codex-prequote-hit-gap-release-f842928-20261031-2-summary.json
#pull /home/chuanming/golive-telephony/codex-latency/prequote-hit-gap-release-f842928-20261031-2/strict.json codex-prequote-hit-gap-release-f842928-20261031-2-strict.json
#pull /home/chuanming/golive-telephony/codex-latency/prequote-hit-gap-release-f842928-20261031-2/quote-audio-diagnostics.json codex-prequote-hit-gap-release-f842928-20261031-2-quote-audio-diagnostics.json
set -euo pipefail
cd "$HOME/golive-telephony"
python3 -B - <<'PY'
import array, fcntl, hashlib, json, math, os, re, shlex, subprocess, time, urllib.error, urllib.request, wave
from collections import Counter
from pathlib import Path

os.umask(0o077)
TARGET = 'f842928'
COMMIT = 'f842928b3dd0862a6f9a6d369b040c21af46101e'
KEY = 'codex-prequote-hit-gap-release-f842928-20261031-2'
home = Path.home()
tele, sim = home/'golive-telephony', home/'golive-sim'
release_lock = (tele/'.codex-deploy.lock').open('a')
fcntl.flock(release_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
RELEASE_PINS = {'release': '/home/chuanming/golive-releases/codex-f842928-20261031-130534', 'files': {'bin/golive-linux-amd64': '083caa6ad1cc8e07f5aac46c9105cc68a8e398e198e46062efb58c361b0a9837', 'bin/crm-linux-amd64': '09bb6dc115884fed6431e2dafcd338b022528610b96037ccc3a09e621870bebf', 'bin/journal-linux-amd64': 'b28307552ca7da0dac97c3a6d483dad7f0a15892bd346b3a0d57868022ce1106', 'CALL-OPERATIONS.md': '8c4fe93aab5a867e50701d6daedb94514767a20961f2aaad88c44c21e543b4e7', 'HANDSET-LATENCY.md': 'd6e99090512fa4b62cb9d7f8556a58eaae3ab849d552cd22ab73ea3304c64ea9', 'VERSION': '7e1ad632632d34031002668647f62b6d9c79e53fdacb30ad20193b64e6923d0d', 'configs/telephony.json': '072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d', 'journal/decisions.json': 'cbeb6eded8c33b9226449a759fa38e5e76c25dac23422f4b5f5de0328ebdc675', 'journal/issues.json': '2f82ab261b8a3debc2c2947ed7fb2ebc8c2ae26793d50b98fe4b553949e155c5', 'journal/retros/out-mvue7jic-wdgq.md': '30eb3adcbbf9e0ff67d856e9cb8f8bab77d4656aeb67bfb5189fafd2865141a6', 'journal/retros/out-mvuklw72-yaxy.md': 'ba820e6f64dd145bf8244510ab74685c7f375e54275a1a5869a060300c566a69', 'journal/retros/out-mvukudco-7zrb.md': '713a1fcba3749f2be21f18bf3ee430d4887dbf64b74fa2075d2a372707816fd5', 'journal/retros/out-mvuluygh-p1vy.md': '89f64024d9978b36cc9fe99f0bcfcb7ce38e91e593b78dbc6ed33b35f2db1303', 'journal/retros/out-mvumm8e2-t8in.md': '71c190b8f8a34ff5ceb99344a7b6eae9bc86d10114d8ded7c7f0abcbff6d6662', 'journal/retros/out-mvumqi79-ghis.md': '3788a3e1baa68a7228311154aa38d8cc44e8e1dce9b478e9ae59d58632db7af0', 'scripts/call-latency.sh': 'e6bd1022ad522792847b809f584ccfa10da7f5eadb32c874cef506ea7e7eadfe', 'scripts/call_latency.py': '41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850', 'scripts/phone-brain.sh': '390bc74c2d3a47cb3be9ccf88c127bcd48b4b218bac08e3785a17a60e331fe8a', 'scripts/sim-retro.py': 'a2169ca0865c1085d5cffb2dfca829904bbe8c889a79f86e270b5283f72d86c7', 'journal/retros/out-mvuoo3dk-dzip.md': 'feead9cfe33570efdd4f99671e3b69df826efcd5069fba05e2af59b81f410fa6', 'journal/retros/out-mvup3e10-odkv.md': '8ea1907529d4fcb59e3d55407d9b2703a40788e9b270a6fbf9f5588898007ab9', 'journal/retros/out-mvupyy53-pvt3.md': 'f6f8dee0e294a71758c68e51af29fa6143b097d6bd0c1ae6651a2cf8e6c8d2be', 'journal/retros/out-mvuqswhl-2dw7.md': '1fd429f4c8bdfce04af34370d84dc10d483f9e5601a353b6a6ba7adbbc1041f2', 'journal/retros/out-mvuqxt3d-i7nq.md': '2d24fbe9dc5d76af0199d99dcd412a4d78de0b45bb1e40fbb307b1624389568c', 'journal/retros/out-mvurvsmk-u9yk.md': '34691527d5a01bf97bf53f856d0eb2284ceb0c1659a09396f5ee22de45200bd4', 'journal/retros/out-mvvvt9l4-cauj.md': 'ce7ab0957bdadc183cf7cfe43437c8753cee5499ce2b7a4f3c1dd46700152639'}, 'profiles': {'8765': {'profile': '/home/chuanming/golive-telephony/configs/telephony.json', 'profile_sha256': '072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d', 'envfile': '/home/chuanming/golive-telephony.env', 'envfile_sha256': '142ee6a2fd67af61666c96d73f40229e7cc66c34e8595af0ea2f8acb8a14209c'}, '9080': {'profile': '/home/chuanming/golive-telephony/configs/tencent-deepseek-minimax.json', 'profile_sha256': '723608ce45ec8e2cf3a091d8c88a7024cb6184e3e006cf4537b7e6160b1a70f2', 'envfile': '/home/chuanming/golive-telephony.env', 'envfile_sha256': '142ee6a2fd67af61666c96d73f40229e7cc66c34e8595af0ea2f8acb8a14209c'}}, 'archive_fingerprints': {'out-mvue7jic-wdgq': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_110834_out-mvue7jic-wdgq', 'manifest_sha256': '4662dabd84651cb08fd05a467dc661d24d3c041649fdce1d2abb88bbebaf652e', 'files': {'audio.wav': {'bytes': 10227244, 'sha256': '2d1143ce13769c4af4f4bf3827651afd612bbb599d952864518440d741680bdd'}, 'transcript.txt': {'bytes': 3567, 'sha256': '49a597f4537d4049c86c092094b2d22cd2a97114cb7e7e798653cb02fe4d9d6c'}, 'call.json': {'bytes': 40097, 'sha256': '2b832c12f95b441b61bf45ec84c5ab750e660ce9fea9b1bc7ce371d2a2d43929'}, 'session.json': {'bytes': 226313, 'sha256': 'e5dae7b522e2738417ecf052cc4c0a6ffe94c00be6a4dec0e66a95f933a36a35'}, 'golive.log': {'bytes': 193896, 'sha256': '56d50bc11f3441f03a2102be4c68237acd3145cee5521db8409480daa1836efd'}}}, 'out-mvuklw72-yaxy': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_140741_out-mvuklw72-yaxy', 'manifest_sha256': 'ff0c6fcc8a5cc69a4d680ae9d50a5c9d2b004383b09e5bd015152a1573022959', 'files': {'audio.wav': {'bytes': 17967132, 'sha256': '8116c9b928b834626135a190afbe7997a7349bb9b18ff5b471ebb67c28d0a10e'}, 'transcript.txt': {'bytes': 6596, 'sha256': '053f5c3c2264627a6c284a334ea5afb1adb5c4d8dc1b39d7164eef34af0c7b61'}, 'call.json': {'bytes': 57744, 'sha256': '1300775a8280ac72859e24216556db5d7596c2b2217a3a804e1a3858c54cba50'}, 'session.json': {'bytes': 403872, 'sha256': '20f36d20ff7eb9068fec70dc18d0dbae89b7b7b9d370f3b098be96f1e5dc002a'}, 'golive.log': {'bytes': 356298, 'sha256': '22e2e8c78ddb1dcb97040e5eee5c4ce137d52905764ccdc9dea4366400a392f4'}}}, 'out-mvukudco-7zrb': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_141417_out-mvukudco-7zrb', 'manifest_sha256': 'f526b7cda256e5c7361b849cdd94d077fcfb7fb741b5023c9fcbb38c67efb518', 'files': {'audio.wav': {'bytes': 5473156, 'sha256': '69ccb17efb8f0426ffa15a32f0c69c43facee8714cceabc2fb8104ce0df0d3dd'}, 'transcript.txt': {'bytes': 1696, 'sha256': '99ade21b4e8a173e9a650f1777214af2b0f84772af41b09b1addab08755e86cb'}, 'call.json': {'bytes': 17603, 'sha256': '8b91419375914fe5c065b32cce22fcbd9078382bddccbd1ad31747253c97f770'}, 'session.json': {'bytes': 144942, 'sha256': 'e832a3002b153a118c04c4b039563f57dc63186563b7138bc07f0900cc17a4f5'}, 'golive.log': {'bytes': 95381, 'sha256': '7198d430918dfc4b4d3e3524ab2d5813a82bda9876e9f9533b34638ac39dab1c'}}}, 'out-mvuluygh-p1vy': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_144244_out-mvuluygh-p1vy', 'manifest_sha256': '2bb33bb51e58d508c7ca17a32ddd7e1d35f3527e5d4b7fe2fe954d5937b71c29', 'files': {'audio.wav': {'bytes': 5993548, 'sha256': '1783e3f255e95590b61b8f2a23de81854c96cddce226437f5121642d60e83cc4'}, 'transcript.txt': {'bytes': 1880, 'sha256': 'e8f0f0197cd630ae9a9f7aedb9421dbd7e6db0969bb815392d7ac49e8df17ce4'}, 'call.json': {'bytes': 18194, 'sha256': 'ed64356e3f5de2e22a331e665e14910c610f511f878562f5d466b702dc031642'}, 'session.json': {'bytes': 156478, 'sha256': 'd861cc86270d30e315160d557b5128fe5846f5a1aa930da4e0286f53b63c9f45'}, 'golive.log': {'bytes': 103353, 'sha256': '759627c2f26e3fc250e1762750cd2c3f8d4813a190575c3373fd96576c73e75f'}}}, 'out-mvumm8e2-t8in': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_150356_out-mvumm8e2-t8in', 'manifest_sha256': '8907a5c1f12d3e904f16ccf7cea328fff7f0b66ff746aab84dcd6c294248968e', 'files': {'audio.wav': {'bytes': 5064140, 'sha256': '5e07d3663a17275beccb4eedca59e965312400b13cf82ba0d02f95ee56179758'}, 'transcript.txt': {'bytes': 1755, 'sha256': '3b09e3044371d2799747d2f3eb962ac92513bf1e6c9554e5d608e0d140431f33'}, 'call.json': {'bytes': 18099, 'sha256': '91947c2b0a97e2ab1ced06c020968db308afed22e59315924e35e4dd83204828'}, 'session.json': {'bytes': 136904, 'sha256': 'd5e2f96fcb4fa3f0dcc49ce2b8b2a5536af604df96d03bda741f5c9aff024d86'}, 'golive.log': {'bytes': 107257, 'sha256': '6b7b52864b95d99f12a4d49975b69796c47c7f7ea02fac96db668c977dc65786'}}}, 'out-mvumqi79-ghis': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_150716_out-mvumqi79-ghis', 'manifest_sha256': '516635b303cbe1686f608ef653e98e3d9be0ae258b6aaa411e2a94c9c83fd5fb', 'files': {'audio.wav': {'bytes': 19035204, 'sha256': 'b844ab19136b95e5e9d3d7d6b48a7c49e07f5e5ea882a978de76fd1b91691bdf'}, 'transcript.txt': {'bytes': 6734, 'sha256': '261698f34e1572974c48b5da30836fa2b7efd6efcf0f1245970c31d544c84b3f'}, 'call.json': {'bytes': 55238, 'sha256': '30764c77f7b3acb2c19cbd0a9061d9d088df3da8a1b87fd1b013369d532ba638'}, 'session.json': {'bytes': 426227, 'sha256': '920cffb0844734eef3c154aec3412bc00b1293ecbfbf48b53633ae7f1a385094'}, 'golive.log': {'bytes': 315368, 'sha256': '5d31dd538b2cfbd4b505f24c537358a270d5b704e49153dc4b6edd9fd22fa201'}}}, 'out-mvuoo3dk-dzip': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_160122_out-mvuoo3dk-dzip', 'manifest_sha256': 'f7f19af93c90df2e0b48fa1137c7a0c56a63b504eea21a8ad61c3994ec57c237', 'files': {'audio.wav': {'bytes': 4873212, 'sha256': 'e484965cdfe135c7386113fe8683241daedd9b2623a55fbab0975c22f6f2760e'}, 'transcript.txt': {'bytes': 1535, 'sha256': '958397b8788e8ff5a9cbe5a3a77fb0ef6a81360fe68c6d7da81c43725b646805'}, 'call.json': {'bytes': 15579, 'sha256': '6b082728f53f74991b7675fa2ce65ed574deb885be87f67e35eaf8fa93749a3b'}, 'session.json': {'bytes': 132246, 'sha256': '1edec67f34e15496959eaa3815a6365ce41743815c1d159846b0ffabf0ce0668'}, 'golive.log': {'bytes': 90653, 'sha256': '364c16dbede7c01cbc85dde1af81f53c687b1609a779a4a1f22453e2ec7703eb'}}}, 'out-mvup3e10-odkv': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_161316_out-mvup3e10-odkv', 'manifest_sha256': 'ef0d2758e2aab6e3d2a93c1db125b37efabfc9846c820496b7ffe69691fc84be', 'files': {'audio.wav': {'bytes': 5607788, 'sha256': '314a6f8729b625fe74c42b37b8b8aed4be4d9512c91195836ca3954efd2d4646'}, 'transcript.txt': {'bytes': 1950, 'sha256': 'e010209aa316afd091924a34a294bb3772083b3405247e612cf323cd8c691902'}, 'call.json': {'bytes': 17067, 'sha256': '9d185190191a375497cdf8e9122c9436ea09babbcb64dadfc1ecf18f91c8c303'}, 'session.json': {'bytes': 142705, 'sha256': 'de741390cf005871faf0012e68ddbab066c8bffa274fb109da5114f010095d27'}, 'golive.log': {'bytes': 101115, 'sha256': 'a9693513f0a868f71f3e6c0921d625f06ff95b600163cb484cac11e4fd6ff7ec'}}}, 'out-mvupyy53-pvt3': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_163748_out-mvupyy53-pvt3', 'manifest_sha256': '2389b43e12e04d287540443863ed25c96091d4b9577dbb89ad68de86a69f8ed6', 'files': {'audio.wav': {'bytes': 6266780, 'sha256': 'd135dcc3e807dbf422f6050be6ab83e30e3486286742f28ac5b2d28efa34bf9a'}, 'call.json': {'bytes': 23204, 'sha256': 'af97415e62392926edc2ac41cb8ae01f5f38fe025f236c9ba5822b4ab3e750d9'}, 'golive.log': {'bytes': 197258, 'sha256': 'f37595e65c9b0ad8beac5deda6dce83f1efe6d74f6ea375a8ae8e8ae79befe78'}, 'session.json': {'bytes': 161834, 'sha256': 'fda2828befc1b07898c6d50c7769c127d1310ef9d1e4ead46398e628216fe242'}, 'transcript.txt': {'bytes': 2173, 'sha256': 'da6307c418d19ca72b4fd7737b99a869c5a59f68794815bd609028667410e7ca'}}}, 'out-mvuqswhl-2dw7': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_170106_out-mvuqswhl-2dw7', 'manifest_sha256': '041b861962919d6403bff11c1b6c464fa5aadee8b886e6cdb672ce2dec45d7ad', 'files': {'audio.wav': {'bytes': 4459580, 'sha256': 'b14b2affc4f8c7eb461b1269439c958b78b61c6b41f83714b82c4be78ab1b5f2'}, 'call.json': {'bytes': 15854, 'sha256': '02fd759cb2b665d0bc4b1edf4f11a11249e4f0bdea3080e3c2f2e970d4d42d10'}, 'golive.log': {'bytes': 132062, 'sha256': '3d38f86ebf35f26537f61a0bce52bce8b21151225c5162177e64ef9cc64a80ea'}, 'session.json': {'bytes': 124316, 'sha256': '5444625ac986f49fcd74b73a2ea1d68aca913051a66adfef5028eb739f3c9c49'}, 'transcript.txt': {'bytes': 1626, 'sha256': '307e44fd058789399655c0a3eb34a6c8fafe8a8bda3a992ef8723c564bb8fc50'}}}, 'out-mvuqxt3d-i7nq': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_170455_out-mvuqxt3d-i7nq', 'manifest_sha256': 'ede914fe04c43612a458857446c9d394c897b8ab8abf46c9509faba95e4bc11b', 'files': {'audio.wav': {'bytes': 19061956, 'sha256': '5251eb289e64705cf677db7cc19099f4e7c2e97778d559bc8136be0e27b1f261'}, 'call.json': {'bytes': 65220, 'sha256': 'bd41c6e5c38bd35cb2ec7fd979c81b4ad11ad16a9b40b84f89ed87975c2bdc79'}, 'golive.log': {'bytes': 520177, 'sha256': 'd0b86d0d273a8602a83848e8d6a31f18c87a8841218578eb8c4d8ef7242e49ab'}, 'session.json': {'bytes': 429362, 'sha256': 'cacc156ed55b7b5cf1410978b1ba29e61ada68bc328d4c6b9f3a22ea1e3b63e2'}, 'transcript.txt': {'bytes': 7514, 'sha256': '909ff55b9ebb478e0fba4960ee59bd697a3fb7ae14b5d88335ff8d047e39e7d2'}}}, 'out-mvurvsmk-u9yk': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-30_173120_out-mvurvsmk-u9yk', 'manifest_sha256': 'b619c9e388fe08b5a840838b746bcf5090478229e4b277e4feafd812dfaa7bcd', 'files': {'audio.wav': {'bytes': 4682804, 'sha256': '005003e0877c4dabd73974af9beeebed237e483d3e54da4c32e759721ff71d35'}, 'call.json': {'bytes': 19917, 'sha256': 'f66bfee4666a6be7d10c7ae92d937d45dba617b0fd2f7cf8187b51d0daa3d858'}, 'golive.log': {'bytes': 170444, 'sha256': 'fa093bb27079a6f6108a88bf5da283f74a925521bfa3bfa2534d2eda1c3d07f5'}, 'session.json': {'bytes': 129046, 'sha256': 'bc9a94fad669ef7f92ea8647b6d633ab6d032f354a51232612959a4fa91fbf0a'}, 'transcript.txt': {'bytes': 1784, 'sha256': '342d303b3df460c2cf6528010d77215252e8ad4cc7e343f43273de7977309510'}}}, 'out-mvvvt9l4-cauj': {'directory': '/home/chuanming/golive-archive/calls/2026/10/2026-10-31_120907_out-mvvvt9l4-cauj', 'manifest_sha256': 'e12542cc8fa23cb44e0e6c29af056b7f0f6eb8aed4d4250b5e096eab749bb0c6', 'files': {'audio.wav': {'bytes': 12340524, 'sha256': 'cc811e94cce5f65d5e52357d683502d3ae459741a85c196e2ffdc1be46004873'}, 'call.json': {'bytes': 46535, 'sha256': 'c3452e0f687264ef5ed3f0bc1c04a6a573019e2212bb150e0a6d02c842091e74'}, 'golive.log': {'bytes': 330216, 'sha256': '5c519bbc87f229929b2a6e65314e9693000dffd9ccbbad9df733fffe602322f4'}, 'session.json': {'bytes': 276684, 'sha256': '75434bcf8da1c4937b7ef7b4775948275c495427d99049dee43257e6f698d3f0'}, 'transcript.txt': {'bytes': 4038, 'sha256': '80b6cff643be9e585733253e973baa19448ae1d4b12dea229e178a1b06d8be0b'}}}}, 'saved_identity': {'instructions_sha256': '639dcecf24dddc73274a7412c017e36258b51679c2fd3ad4e72a595453aa8d25', 'greeting_sha256': 'fa7c33a6b9c732413d932a7453c5c85d4936fcdd8515822bf92e3fc07e2f4df8', 'voice_sha256': '65d9d6c5c7c2d5c29e38b777edf8da1dbd764f67deeeaa230724967cafe4e684', 'call': 'out-mvvvt9l4-cauj', 'session': 'sess_2da1b84f88531f040b7744ad'}, 'receipt_sha256': {'build': '27a15ed8c4dccd7d4eb3b46463f9f8bfac2bc3c1876135f97562405a8a3d5a46', 'ci': '57395deb562a099eb2c11e9af8a3937680da534d615e2022c9932b6e25650939', 'preflight': 'a992a304bfcb0d9746eb7277f3c8b193ca8c2f9f05617bc160b7de0ead37535e', 'deployment': '9f832dd1989ad97f1581972209d7d175f913c022349ba56d2d17dee08019a038', 'postverify': '74b94f2a1448bee267b98d68fc4587207c6e3f9824ae3228b000da0707083d75'}, 'remote_receipts': {'/home/chuanming/golive-releases/codex-f842928-20261031-130534/deployment.json': '9f832dd1989ad97f1581972209d7d175f913c022349ba56d2d17dee08019a038', '/home/chuanming/golive-telephony/codex-latency/preflight-gap-release-20261031-2.json': 'a992a304bfcb0d9746eb7277f3c8b193ca8c2f9f05617bc160b7de0ead37535e', '/home/chuanming/golive-telephony/codex-latency/verify-gap-release-20261031-2-f842928.json': '74b94f2a1448bee267b98d68fc4587207c6e3f9824ae3228b000da0707083d75'}}
out = tele/'codex-latency/prequote-hit-gap-release-f842928-20261031-2'
require_out_parent = out.parent
if out.is_symlink(): raise RuntimeError('attempt directory linked')
out.mkdir(exist_ok=True)
fd = os.open(out.parent, os.O_RDONLY | os.O_DIRECTORY)
try: os.fsync(fd)
finally: os.close(fd)
attempt = out/'attempt.json'

def require(ok, message):
    if not ok:
        raise RuntimeError(message)

def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b''):
            h.update(chunk)
    return h.hexdigest()

"""Sanitized quote preparation observations; never infer handset latency."""
import json
import re
import shlex
from collections import Counter


def diagnostic_int(value):
    if isinstance(value, str) and re.fullmatch(r'[0-9]{1,12}', value):
        value = int(value)
    return value if type(value) is int and 0 <= value < 10**12 else None


def diagnostic_bool(value):
    if value is True or value == 'true':
        return True
    if value is False or value == 'false':
        return False
    return None


def diagnostic_id(value, prefix):
    return value if isinstance(value, str) and re.fullmatch(prefix + r'[A-Za-z0-9_-]{1,80}', value) else '-'


def quote_audio_diagnostics(lines, session, strict):
    counts = Counter()
    preparations, enqueues, enables = [], [], []
    for line in lines:
        try:
            row = json.loads(line) if line.lstrip().startswith('{') else dict(
                word.split('=', 1) for word in shlex.split(line) if '=' in word)
        except (ValueError, TypeError):
            continue
        if not isinstance(row, dict) or row.get('session') != session:
            continue
        message = row.get('msg')
        if message == 'quote audio: bounded preparation enabled':
            enables.append({'max_attempts': diagnostic_int(row.get('max_attempts'))})
        elif message == 'quote audio: preparation':
            status = row.get('status')
            status = status if status in ('ready', 'failed', 'capacity', 'retired') else 'unknown'
            attempted = diagnostic_bool(row.get('attempted'))
            counts[status] += 1
            counts['attempted'] += int(attempted is True)
            counts['not_attempted'] += int(attempted is False)
            counts['unknown_attempted'] += int(attempted is None)
            preparations.append({'status': status, 'attempted': attempted,
                **{key: diagnostic_int(row.get(key)) for key in ('chars', 'bytes', 'ms')}})
        elif message == 'speak: prepared quote segment played':
            enqueues.append({'turn': diagnostic_id(row.get('turn'), r'(?:item_|turn_)'),
                **{key: diagnostic_int(row.get(key)) for key in ('chars', 'audio_ms', 'lead_ms')}})
    turns = Counter(row['turn'] for row in enqueues if row['turn'] != '-')
    readouts = []
    for row in strict.get('quote_readout_rows', []):
        if not isinstance(row, dict) or row.get('session') != session:
            continue
        clean = {key: diagnostic_id(row.get(key), prefix) for key, prefix in (
            ('task', r'(?:task_|t)'), ('origin_turn', r'(?:item_|turn_)'), ('result_turn', r'(?:item_|turn_)'))}
        clean['status'] = row.get('status') if row.get('status') in ('observed', 'missing', 'unverified') else 'unverified'
        clean.update({key: diagnostic_int(row.get(key)) for key in (
            'end_silence_ms', 'origin_to_result_start_ms', 'child_first_readout_ms', 'total_ms')})
        clean['prepared_segment_enqueues_in_result_turn'] = turns[clean['result_turn']]
        readouts.append(clean)
    observed = [row for row in readouts if row['status'] == 'observed']
    issues = []
    if len(enables) != 1 or any(row['max_attempts'] != 4 for row in enables):
        issues.append('expected_one_enabled_budget_4_event')
    if counts['attempted'] > 4:
        issues.append('observed_attempts_exceed_call_budget')
    if counts['unknown_attempted'] or counts['unknown']:
        issues.append('unrecognized_preparation_fields')
    return {
        'scope': 'Selected session only. Prepared cache-hit events record enqueue, not audible output. Only strict observed readouts establish server output; neither proves handset playout.',
        'enabled_events': enables,
        'preparation_events': len(preparations),
        'preparation_counts': {key: counts[key] for key in ('attempted', 'not_attempted', 'unknown_attempted', 'ready', 'failed', 'capacity', 'retired', 'unknown')},
        'preparations': preparations,
        'prepared_cache_hit_enqueue_events': len(enqueues),
        'prepared_cache_hit_enqueues': enqueues,
        'strict_observed_readouts': len(observed),
        'strict_observed_readouts_with_prepared_enqueue': sum(row['prepared_segment_enqueues_in_result_turn'] > 0 for row in observed),
        'strict_readout_rows': readouts,
        'diagnostic_issues': issues,
        'handset_target_proven': False,
    }


def save_state():
    temporary = out/'attempt.tmp'
    require(not temporary.exists() and not temporary.is_symlink(), 'attempt temporary exists; inspect before retry')
    with temporary.open('x') as stream:
        stream.write(json.dumps(state, indent=2)+'\n')
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, attempt)
    fd = os.open(out, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)

def get(url, token=None, method='GET'):
    headers = {'Authorization': 'Bearer '+token} if token else {}
    req = urllib.request.Request(url, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            raw = response.read(16 << 20)
            return json.loads(raw) if raw else {}
    except (OSError, ValueError):
        raise RuntimeError('HTTP request failed; response body withheld') from None

# Embedded in the reviewed simulator job; all operations here are read-only.
def required_file_hash(path, expected):
    require(path.is_file() and not path.is_symlink() and digest(path) == expected,
            'pinned release evidence changed or linked')


def release_guard():
    release = Path(RELEASE_PINS['release'])
    require(release.resolve(strict=True) == release and release.parent == home/'golive-releases',
            'release directory differs')
    for path, expected in RELEASE_PINS['remote_receipts'].items():
        required_file_hash(Path(path), expected)
    for name, expected in RELEASE_PINS['files'].items():
        required_file_hash(tele/name, expected)
    processes = {}
    for port, service in ((8765, 'golive'), (9080, 'golive'), (8791, 'crm'), (8790, 'journal')):
        owners = set(re.findall(r'pid=(\d+)', subprocess.check_output(['ss', '-lntpH', 'sport = :'+str(port)], text=True)))
        require(len(owners) == 1, 'release service identity ambiguous')
        proc = Path('/proc')/owners.pop()
        require(proc.stat().st_uid == os.getuid() and digest(proc/'exe') == RELEASE_PINS['files']['bin/'+service+'-linux-amd64'],
                'release service executable differs')
        processes[str(port)] = (proc.name, (proc/'stat').read_text().rsplit(')', 1)[1].split()[19])
        if port in (8765, 9080):
            health = get('http://127.0.0.1:'+str(port)+'/healthz')
            require(health.get('status') == 'ok' and health.get('version') == TARGET and health.get('sessions') == 0,
                    'phone or demo is busy, unhealthy or changed')
            args = [os.fsdecode(x) for x in (proc/'cmdline').read_bytes().split(b'\0') if x]
            profiles = [v.split('=', 1)[1] for v in args if v.startswith('-profile=')]
            profiles += [args[i+1] for i, v in enumerate(args[:-1]) if v == '-profile']
            require(len(profiles) == 1, 'release profile option ambiguous')
            profile = ((proc/'cwd').resolve()/profiles[0]).resolve(strict=True)
            expected = RELEASE_PINS['profiles'][str(port)]
            require(profile == Path(expected['profile']), 'release active profile path differs')
            required_file_hash(profile, expected['profile_sha256'])
    for expected in RELEASE_PINS['profiles'].values():
        required_file_hash(Path(expected['envfile']), expected['envfile_sha256'])
    archive_root = (home/'golive-archive').resolve(strict=True)
    for call, record in RELEASE_PINS['archive_fingerprints'].items():
        source = Path(record['directory'])
        require(source.resolve(strict=True) == source and source.is_relative_to(archive_root), 'historical archive escaped root')
        required_file_hash(source/'manifest.json', record['manifest_sha256'])
        manifest = json.loads((source/'manifest.json').read_text())
        require(manifest.get('id') == call and manifest.get('files') == record['files'], 'historical archive identity differs')
        for name, stamp in record['files'].items():
            p = source/name
            required_file_hash(p, stamp['sha256'])
            require(p.stat().st_size == stamp['bytes'], 'historical archive file size changed')
    return processes


def saved_context_before():
    require(kept_path.is_file() and not kept_path.is_symlink(), 'saved context missing or linked')
    kept = json.loads(kept_path.read_text())
    expected = RELEASE_PINS['saved_identity']
    require(kept.get('call') == expected['call'], 'saved call changed before this reviewed attempt')
    record = RELEASE_PINS['archive_fingerprints'][expected['call']]
    source = Path(record['directory'])
    call = json.loads((source/'call.json').read_text())
    session = json.loads((source/'session.json').read_text())
    require(call.get('id') == expected['call'] and call.get('session') == session.get('id') == expected['session'],
            'saved call/session pairing differs')
    request = session.get('session', {})
    reference = {'instructions': request.get('instructions'), 'voice': request.get('audio', {}).get('output', {}).get('voice', ''),
                 'greeting': call.get('greeting')}
    for key, value in reference.items():
        actual = kept.get(key, '' if key == 'voice' else None)
        require(isinstance(actual, str) and actual == value and hashlib.sha256(actual.encode()).hexdigest() == expected[key+'_sha256'],
                'saved prompt, voice or greeting differs from pinned archive')
    return kept

require(not attempt.exists() and not attempt.is_symlink(), 'attempt already exists; inspect it and never blindly redial')
scenario = sim/'scenarios/custom/sim-prequote-hit.json'
require(scenario.is_file() and not scenario.is_symlink(), 'documented existing scenario absent or symlinked; no scenario created')
scen = json.loads(scenario.read_text())
require(scen.get('id') == 'sim-prequote-hit', 'existing scenario identity differs')
duration_ms = scen.get('max_duration_ms')
require(type(duration_ms) is int and 0 < duration_ms <= 600000, 'scenario duration is absent or exceeds bounded job')
scenario_sha = digest(scenario)
EXPECTED_SCENARIO_SHA256 = '472dce6497836421303731edfcf9dc7b64c61658bcf02f375d064ac72e447894'
require(re.fullmatch(r'[0-9a-f]{64}', EXPECTED_SCENARIO_SHA256) is not None and scenario_sha == EXPECTED_SCENARIO_SHA256,
        'scenario hash not reviewed or changed; no reservation made')
kept_path = home/'golive-last-call.json'
release_processes = release_guard()
kept = saved_context_before()
require(isinstance(kept.get('instructions'), str) and kept['instructions'], 'saved business prompt is absent')
prompt_sha = hashlib.sha256(kept['instructions'].encode()).hexdigest()
require(prompt_sha == '639dcecf24dddc73274a7412c017e36258b51679c2fd3ad4e72a595453aa8d25', 'saved prompt changed; no reservation or call made')
require((tele/'VERSION').read_text().strip() == COMMIT, 'deployed full commit differs')
health = get('http://127.0.0.1:8765/healthz')
require(health.get('status') == 'ok' and health.get('sessions') == 0 and health.get('version') == TARGET,
        'phone service is busy, unhealthy, or on another build')
require((tele/'.phone-brain.record').is_file(), 'recording marker is absent')
# Pin the separately enabled policy before reserving or dialing.
crm_rows = subprocess.check_output(['ss', '-lntpH', 'sport = :8791'], text=True)
crm_pids = set(re.findall(r'pid=(\d+)', crm_rows))
require(len(crm_pids) == 1, 'CRM identity is ambiguous')
crm_proc = Path('/proc')/crm_pids.pop()
crm_args = [os.fsdecode(x) for x in (crm_proc/'cmdline').read_bytes().split(b'\0') if x]
def crm_option(name):
    for index, value in enumerate(crm_args):
        if value == name and index+1 < len(crm_args):
            return crm_args[index+1]
        if value.startswith(name+'='):
            return value.split('=', 1)[1]
    raise RuntimeError('CRM data directory option absent')
crm_dir = ((crm_proc/'cwd').resolve()/crm_option('-dir')).resolve()
crm_token = (crm_dir/'api.token').read_text().strip()
crm_identity = (crm_proc.name, (crm_proc/'stat').read_text().rsplit(')', 1)[1].split()[19])
CRM_SHA256 = '09bb6dc115884fed6431e2dafcd338b022528610b96037ccc3a09e621870bebf'
# The v7 policy was enabled on the previous runtime. Preserve that exact receipt,
# independently of the new runtime whose VERSION/health/executable hash is checked.
POLICY_RECEIPT_CANDIDATE = '380c1a7350ec73094f01cc19006acff08b587719'
PINNED_POLICY = {'receipt_sha256': '6668f7ea49565f1d92bfc1e20b4515043d211b94da8a1013de6c0b0a9102f5c9', 'expected': {'version': 7, 'sha': 'f2142bd9cddd', 'body_sha256': 'f2142bd9cddd813e00cb898f837206f8975c59d1ef4257aa12d143eecf1d1afb'}, 'protected_configs': {'hangup-rules': {'version': 3, 'sha': '04a2f49a1de6', 'body_sha256': '04a2f49a1de6c22b0708c999df70c63279899d81947ae5e9e73f99a696ebca9a'}, 'openers': {'version': 3, 'sha': '1dc666c8cc12', 'body_sha256': '1dc666c8cc129ee8bad42be09dc2a216820ce4aa3dc5bb7942c6b2031e2b8bd2'}, 'quote-api': {'version': 4, 'sha': 'e0f534988eda', 'body_sha256': 'e0f534988eda239825ce70184b6f0d371d1b68f6f939affdfb5065fa453b8ef0'}, 'quote-rules': {'version': 2, 'sha': '854a7d3e9e25', 'body_sha256': '854a7d3e9e258472d356f21f5cfc6477c7afd2cbd429401363464302d1b2973b'}, 'quote-speech': {'version': 1, 'sha': '4a155d04c900', 'body_sha256': '4a155d04c9004102adf8a9d4a3954677f5c6cb41f0f9aef8d82df09ab628d7ef'}}, 'follow_selection_max': 8}
receipt_dir = tele/'codex-latency/follow-selection-380c1a7-20261030-1'
receipt_path = receipt_dir/'enable-attempt.json'
receipt_sha = digest(receipt_path)
require(receipt_sha == PINNED_POLICY['receipt_sha256'], 'enabled policy receipt differs from reviewed baseline')
receipt = json.loads(receipt_path.read_text())
require(receipt.get('operation') == 'enable' and receipt.get('stage') == 'verified' and
        receipt.get('candidate') == POLICY_RECEIPT_CANDIDATE and receipt.get('change_path') == 'plans.standard.prequote.follow_selection_max' and
        type(receipt.get('value')) is int and receipt['value'] == 8,
        'original v7 enablement has not been verified')
require(receipt['expected'] == PINNED_POLICY['expected'] and receipt['protected_configs'] == PINNED_POLICY['protected_configs'],
        'original v7 policy or protected configuration snapshot differs')
expected_policy = PINNED_POLICY['expected']
require(receipt['baseline']['version'] == 6 and receipt['baseline']['sha'] == 'f50842970ace' and
        expected_policy['version'] == 7 and expected_policy['sha'] == expected_policy['body_sha256'][:12],
        'enable receipt baseline or target differs')
require(digest(receipt_dir/'original-v6.json') == receipt['baseline']['body_sha256'] and
        digest(receipt_dir/'candidate-v7.json') == expected_policy['body_sha256'], 'saved enable payload changed')
for name, expected_version, expected_sha in (('quote-api', 4, 'e0f534988eda'), ('quote-rules', 2, '854a7d3e9e25')):
    protected = receipt['protected_configs'].get(name, {})
    require(protected.get('version') == expected_version and protected.get('sha') == expected_sha,
            'enable receipt protected baseline differs')

def current_quote_policy():
    require(digest(receipt_path) == receipt_sha, 'enable receipt changed during replay')
    require(not (receipt_dir/'rollback-attempt.json').exists(), 'configuration rollback was attempted')
    require((tele/'VERSION').read_text().strip() == COMMIT, 'runtime commit changed')
    owners = set(re.findall(r'pid=(\d+)', subprocess.check_output(['ss', '-lntpH', 'sport = :8791'], text=True)))
    require(owners == {crm_identity[0]} and
            (crm_proc/'stat').read_text().rsplit(')', 1)[1].split()[19] == crm_identity[1] and
            digest(crm_proc/'exe') == CRM_SHA256, 'CRM candidate identity changed')
    def stamp_config(name):
        require(re.fullmatch(r'[a-z0-9_-]{1,64}', name) is not None, 'unexpected configuration name')
        value = get('http://127.0.0.1:8791/api/configs/'+name, crm_token)['config']
        require(value.get('name') == name and type(value.get('version')) is int, 'configuration identity invalid')
        raw_path = crm_dir/'configs'/name/('v%04d.json' % value['version'])
        raw_sha = digest(raw_path)
        require(raw_sha[:12] == value.get('sha') and json.loads(raw_path.read_text()) == value.get('body'),
                'configuration API and stored bytes differ')
        return value, {'version':value['version'], 'sha':value['sha'], 'body_sha256':raw_sha}
    rows = get('http://127.0.0.1:8791/api/configs', crm_token)['configs']
    names = [row['name'] for row in rows if row['name'] != 'quote-plans']
    require(len(names) == len(set(names)) and set(names) == set(receipt['protected_configs']),
            'protected configuration inventory changed')
    require({name:stamp_config(name)[1] for name in names} == receipt['protected_configs'],
            'a protected configuration changed after enablement')
    policy, stamp = stamp_config('quote-plans')
    limit = policy.get('body', {}).get('plans', {}).get('standard', {}).get('prequote', {}).get('follow_selection_max')
    require(stamp == expected_policy and policy.get('by') == 'codex' and type(limit) is int and limit == 8,
            'exact reviewed follow-selection policy is not active')
    return {**stamp, 'follow_selection_max':8, 'enable_receipt_sha256':receipt_sha, 'enable_receipt_candidate':POLICY_RECEIPT_CANDIDATE,
            'protected_configurations_verified':len(names)}
quote_policy = current_quote_policy()

listeners = subprocess.check_output(['ss', '-lntpH', 'sport = :8765'], text=True)
pids = set(re.findall(r'pid=(\d+)', listeners))
require(len(pids) == 1, 'phone process identity is ambiguous')
pid = pids.pop()
env = dict(x.split(b'=', 1) for x in (Path('/proc')/pid/'environ').read_bytes().split(b'\0') if b'=' in x)
require(env.get(b'GOLIVE_REALTIME_STORE_CALLS') == b'1' and env.get(b'GOLIVE_STORE_DIR') and
        env.get(b'GOLIVE_STORE_TTL_HOURS') == b'720', 'active recording configuration differs')
PHONE_SHA256 = '083caa6ad1cc8e07f5aac46c9105cc68a8e398e198e46062efb58c361b0a9837'
PROFILE_SHA256 = '072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'
LATENCY_SCRIPT_SHA256 = '41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850'
PHONE_SCRIPT_SHA256 = '390bc74c2d3a47cb3be9ccf88c127bcd48b4b218bac08e3785a17a60e331fe8a'
phone_proc = Path('/proc')/pid
phone_identity = (pid, (phone_proc/'stat').read_text().rsplit(')', 1)[1].split()[19])

def current_runtime():
    require((tele/'VERSION').read_text().strip() == COMMIT, 'runtime commit changed')
    owners = set(re.findall(r'pid=(\d+)', subprocess.check_output(['ss', '-lntpH', 'sport = :8765'], text=True)))
    require(owners == {phone_identity[0]} and
            (phone_proc/'stat').read_text().rsplit(')', 1)[1].split()[19] == phone_identity[1],
            'phone process changed during replay')
    require(digest(phone_proc/'exe') == PHONE_SHA256, 'phone executable differs from reviewed manifest')
    argv = [os.fsdecode(x) for x in (phone_proc/'cmdline').read_bytes().split(b'\0') if x]
    profiles = [v.split('=', 1)[1] for v in argv if v.startswith('-profile=')]
    profiles += [argv[i+1] for i, v in enumerate(argv[:-1]) if v == '-profile']
    require(len(profiles) == 1, 'phone profile option ambiguous')
    profile = ((phone_proc/'cwd').resolve()/profiles[0]).resolve(strict=True)
    require(profile == (tele/'configs/telephony.json').resolve(strict=True) and
            digest(profile) == PROFILE_SHA256, 'runtime profile differs from exact candidate')
    def unique_pairs(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'duplicate profile key')
            result[key] = value
        return result
    config = json.loads(profile.read_text(), object_pairs_hook=unique_pairs)
    budget = config.get('duplex', {}).get('quote_readout_warm_max')
    require(type(budget) is int and budget == 4, 'quote audio trial budget differs')
    require(digest(tele/'scripts/call_latency.py') == LATENCY_SCRIPT_SHA256 and
            digest(tele/'scripts/phone-brain.sh') == PHONE_SCRIPT_SHA256, 'replay helper differs from candidate')
    return {'profile_sha256': PROFILE_SHA256, 'quote_readout_warm_max': 4,
            'phone_sha256': PHONE_SHA256, 'crm_sha256': CRM_SHA256}

runtime = current_runtime()

jbase = 'http://192.168.71.9:8790'
overview = get(jbase+'/api/overview')
require(overview.get('recording') is True and overview.get('retention_hours') == 720 and
        overview.get('target_ms') == 1200, 'journal recording configuration differs')
token = ''
for line in (sim/'tokens.env').read_text().splitlines():
    if line.startswith('SIM_CONTROL_TOKEN='):
        token = line.split('=', 1)[1].strip().strip('"\'')
require(bool(token), 'simulator control token absent')

def sim_request(path, method='GET'):
    return get('http://127.0.0.1:18865'+path, token, method)

sim_request('/readyz')
runs = sim_request('/sim/v1/runs')
runs = runs.get('runs', []) if isinstance(runs, dict) else runs
terminal = {'completed', 'rejected', 'expired', 'failed', 'cancelled', 'aborted'}
require(isinstance(runs, list) and all(r.get('phase') in terminal for r in runs), 'existing simulator reservation or call')
state = {'build': TARGET, 'commit': COMMIT, 'scenario': 'sim-prequote-hit',
         'scenario_file_sha256': scenario_sha, 'instructions_sha256': prompt_sha,
         'quote_policy': quote_policy, 'runtime': runtime, 'kind': 'simulated', 'stage': 'reservation_requested', 'idempotency_key': KEY}
with attempt.open('x') as f:
    f.write(json.dumps(state, indent=2)+'\n')
    f.flush()
    os.fsync(f.fileno())
fd = os.open(out, os.O_RDONLY | os.O_DIRECTORY)
try: os.fsync(fd)
finally: os.close(fd)
print(json.dumps({**state, 'max_duration_ms': duration_ms}), flush=True)
run = None
completed = False
try:
    prepared = subprocess.run(['bash', str(sim/'scripts/sim-brain.sh'), 'prepare', str(scenario), KEY],
                              capture_output=True, text=True, timeout=40)
    match = re.search(r'^run_id:\s+(sim-[A-Za-z0-9_-]+)', prepared.stdout, re.M)
    require(prepared.returncode == 0 and match is not None, 'reservation failed or outcome unknown; no dial attempted')
    run = match.group(1)
    state.update(stage='reserved', run_id=run)
    save_state()
    require(sim_request('/sim/v1/runs/'+run).get('phase') in {'prepared', 'waiting'}, 'owned reservation is not waiting')
    require(digest(scenario) == scenario_sha, 'scenario changed during reservation')
    require(release_guard() == release_processes, 'release service changed before dial')
    again = saved_context_before()
    require(all(again.get(k) == kept.get(k) for k in ('instructions', 'voice', 'greeting')), 'saved prompt changed during reservation')
    check = get('http://127.0.0.1:8765/healthz')
    require(check.get('status') == 'ok' and check.get('sessions') == 0 and check.get('version') == TARGET,
            'service became busy or changed before dial')
    require(current_quote_policy() == quote_policy, 'quote policy changed before dial')
    require(current_runtime() == runtime, 'runtime changed before dial')
    state['stage'] = 'dial_requested'
    save_state()
    dial = subprocess.run(['bash', str(tele/'scripts/phone-brain.sh'), 'call', 'sim', run],
                          capture_output=True, text=True, timeout=60,
                          env=dict(os.environ, LAST_CALL=str(kept_path), SIM_DIR=str(sim), ARIA_ENV=str(home/'aria/.env'), ARIA_URL='http://127.0.0.1:3000'))
    match = re.search(r'paired: call (out-[A-Za-z0-9-]+) <-> run '+re.escape(run), dial.stdout)
    require(dial.returncode == 0 and match is not None, 'dial failed or outcome unknown; never redial this attempt')
    call = match.group(1)
    state.update(stage='dial_accepted', call_id=call)
    save_state()
    print(json.dumps({'call': call, 'run': run, 'kind': 'simulated'}), flush=True)
    deadline = time.monotonic()+duration_ms/1000+60
    last_phase = None
    while time.monotonic() < deadline:
        phase = sim_request('/sim/v1/runs/'+run).get('phase')
        if phase != last_phase:
            print(json.dumps({'run': run, 'phase': phase if phase in terminal|{'prepared','waiting','ringing','answering','connected','ending','connecting','active','running'} else 'other'}), flush=True)
            last_phase = phase
        if phase in terminal:
            completed = True
            break
        time.sleep(3)
    require(completed, 'owned simulator call exceeded bounded wait')
    state['stage'] = 'call_ended'
    save_state()
    deadline = time.monotonic()+120
    while True:
        data = get(jbase+'/api/calls/'+call)
        current, archive = data['call'], data.get('archive', {})
        if current.get('ended_at') and not current['ended_at'].startswith('0001-') and archive.get('archived') and archive.get('audio'):
            break
        require(time.monotonic() < deadline, 'selected call archive is not ready; collect this same call later')
        time.sleep(5)
    require(current['id'] == call and current.get('build') == TARGET and data.get('sim_run') == run,
            'journal call, build, or simulator pairing differs')
    session = current['session']
    require(re.fullmatch(r'sess_[A-Za-z0-9_-]+', session) is not None, 'session identity invalid')
    source = Path(archive['dir']).resolve()
    require(source.is_relative_to((home/'golive-archive').resolve()), 'archive path outside expected root')
    require(source.is_dir() and (source/'manifest.json').is_file() and not (source/'manifest.json').is_symlink(), 'archive manifest missing or linked')
    manifest = json.loads((source/'manifest.json').read_text())
    require(manifest['id'] == call and manifest['session'] == session and manifest.get('audio'), 'archive manifest identity differs')
    for name in ('audio.wav', 'transcript.txt', 'call.json', 'session.json', 'golive.log'):
        item, expected = source/name, manifest['files'][name]
        require(item.is_file() and not item.is_symlink(), 'archive file missing or linked: '+name)
        require(item.stat().st_size == expected['bytes'] and expected['bytes'] > 0 and digest(item) == expected['sha256'],
                'archive checksum or size mismatch: '+name)
    meta = json.loads((source/'session.json').read_text())
    require(meta.get('id') == session and meta.get('closed_at'), 'recorded session not closed')
    recorded_prompt = meta.get('session', {}).get('instructions', '')
    require(hashlib.sha256(recorded_prompt.encode()).hexdigest() == prompt_sha, 'recorded prompt differs')
    require(meta.get('session', {}).get('audio', {}).get('output', {}).get('voice', '') == kept.get('voice', ''), 'recorded voice differs')
    require(current.get('greeting') == kept.get('greeting'), 'recorded greeting differs')
    speakers = Counter(x.get('who') for x in current.get('transcript', []) if x.get('text', '').strip())
    require(speakers['caller'] and speakers['assistant'], 'two-sided transcript absent')
    signal = [False, False]
    with wave.open(str(source/'audio.wav'), 'rb') as wav:
        require(wav.getnchannels() == 2 and wav.getsampwidth() == 2, 'expected stereo PCM16 recording')
        rate, seconds = wav.getframerate(), wav.getnframes()/wav.getframerate()
        while chunk := wav.readframes(16000):
            samples = array.array('h', chunk)
            for side in (0, 1):
                signal[side] |= any(samples[side::2])
    require(all(signal), 'one recording channel is silent')
    result = subprocess.run(['python3', '-B', 'scripts/call_latency.py', str(source/'golive.log'),
                             '--gate', '--metric', 'both', '--target-ms', '1200', '--min-turns', '20', '--json'],
                            capture_output=True, text=True, timeout=30)
    require(result.returncode in (0, 1), 'strict report failed to run')
    strict = json.loads(result.stdout)
    # Persist the raw strict report before inspecting its shape or API agreement.
    (out/'strict.json').write_text(json.dumps(strict, indent=2)+'\n')
    counts = strict.get('counts', {})
    required_counts = ('answered', 'interrupted_answers', 'missing_output_records', 'missing', 'unverified', 'duplicates', 'malformed')
    require(all(type(counts.get(k)) is int and counts[k] >= 0 for k in required_counts), 'new strict accounting schema incomplete; raw strict report and archive retained')
    require(counts['interrupted_answers'] <= counts['answered'], 'invalid interrupted answer count; raw strict report and archive retained')
    evidence = current.get('latency', {})
    try:
        journal_matches_strict = evidence.get('version') == 4 and all(
            evidence.get(key, 0 if key == 'missing_output_records' else None) == counts[key]
            for key in ('missing', 'unverified', 'duplicates', 'missing_output_records')) and all(
            all(strict[metric].get(k) == v for k, v in evidence[metric].items())
            for metric in ('first_audio', 'first_content', 'tool_result', 'quote_readout'))
    except (AttributeError, KeyError, TypeError):
        journal_matches_strict = False
    output_accounting = {'latency_evidence_version': 4,
        'answered_including_interrupted': counts['answered'],
        'completed_answers': counts['answered'] - counts['interrupted_answers'],
        'interrupted_answers': counts['interrupted_answers'],
        'missing_output_records': counts['missing_output_records'],
        'unverified': counts['unverified'], 'duplicates': counts['duplicates'],
        'journal_matches_strict': journal_matches_strict,
        'passed': journal_matches_strict and all(counts[k] == 0 for k in ('missing_output_records', 'missing', 'unverified', 'duplicates', 'malformed')),
        'scope': 'All complete and truncated server-output rows retained; no handset proof'}
    diagnostics = quote_audio_diagnostics((source/'golive.log').read_text(errors='replace').splitlines(), session, strict)
    (out/'quote-audio-diagnostics.json').write_text(json.dumps(diagnostics, indent=2)+'\n')
    # Only report task identifiers and booleans; never copy result text/prices.
    defaults = {}
    for message in meta.get('history', []):
        if message.get('role') != 'system':
            continue
        content = message.get('content', '')
        match = re.match(r'^【后台任务结果】(t[0-9]+)（quote_total）已完成，结果：', content)
        if not match:
            continue
        try:
            result_object, _ = json.JSONDecoder().raw_decode(content[match.end():])
        except (ValueError, TypeError):
            continue
        if isinstance(result_object, dict):
            task = match.group(1)
            defaults[task] = None if task in defaults else result_object.get('default') is True
    skips = {}
    for line in (source/'golive.log').read_text(errors='replace').splitlines():
        try:
            record = json.loads(line) if line.lstrip().startswith('{') else dict(word.split('=',1) for word in shlex.split(line) if '=' in word)
        except (ValueError, TypeError):
            continue
        if (isinstance(record, dict) and record.get('session') == session and
                record.get('msg') == "tools: delivering a task's result" and record.get('tool') == 'quote_total'):
            default_flag = record.get('quote_result_default')
            if default_flag in (True, 'true'):
                defaults[record.get('task')] = True
            elif default_flag in (False, 'false'):
                defaults[record.get('task')] = False
            skips[record.get('task')] = {
                'skipped':record.get('posthook_skipped') in (True, 'true'),
                'guarded':record.get('posthook_skipped') in (True, 'true') and
                          record.get('interrupting') in (False, 'false') and
                          record.get('memory_changed') in (False, 'false')}
    exercised = []
    for row in strict['quote_readout_rows']:
        task = row['task']
        exercised.append({'task':task, 'status':row['status'], 'total_ms':row['total_ms'],
                          'posthook_skipped':skips.get(task, {}).get('skipped'),
                          'guarded_posthook_skip':skips.get(task, {}).get('guarded'), 'default_quote':defaults.get(task)})
    observed = [row for row in exercised if row['status'] == 'observed']
    nondefault_skips = sum(row['guarded_posthook_skip'] is True and row['default_quote'] is False for row in observed)
    exercise = {'minimum_observed_readouts':2, 'observed_readouts':len(observed),
                'minimum_nondefault_skips':1, 'passed':len(observed)>=2 and nondefault_skips>=1,
                'observed_with_posthook_skipped':sum(row['posthook_skipped'] is True for row in observed),
                'confirmed_nondefault_skip':nondefault_skips,
                'rows':exercised,
                'scope':'Targeted quotation-path exercise; strict n20 report remains separate and handset latency remains unproven'}
    report = sim_request('/sim/v1/runs/'+run+'/report')
    require(report.get('run_id') == run and report.get('scenario_id') == 'sim-prequote-hit', 'receiver report identity differs')
    (out/'receiver-report-private.json').write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
    gaps = report.get('assistant_response_gaps', [])
    values = sorted(g['value_ms'] for g in gaps if g.get('valid') is True and
                    g.get('evidence') == 'application_audio_boundary' and type(g.get('value_ms')) is int and g['value_ms'] >= 0)
    def quantile(q):
        return values[max(0, math.ceil(len(values)*q)-1)] if values else None
    after = json.loads(kept_path.read_text())
    preserved = all(after.get(k) == kept.get(k) for k in ('instructions', 'voice', 'greeting'))
    require(after.get('call') == call, 'new saved call does not match collected call')
    require(release_guard() == release_processes, 'release service or historical evidence changed during call')
    require(preserved and digest(scenario) == scenario_sha, 'saved prompt or scenario changed during call')
    require(current_quote_policy() == quote_policy, 'quote policy changed during call')
    require(current_runtime() == runtime, 'runtime changed during call')
    summary = {'runtime': runtime, 'historical_archives_verified': 13, 'historical_archive_files_verified': 65,
               'saved_context_before': RELEASE_PINS['saved_identity'], 'saved_context_after_call': call,
               'stored_voice_preserved': preserved, 'stored_greeting_preserved': preserved,
               'release_receipts_sha256': RELEASE_PINS['receipt_sha256'], 'quote_audio': diagnostics,
               'evidence_sha256': {name:digest(out/name) for name in ('strict.json', 'quote-audio-diagnostics.json', 'receiver-report-private.json')},
               'archive_manifest_sha256': digest(source/'manifest.json'),
               'quote_policy': quote_policy, 'kind': 'simulated', 'build': TARGET, 'commit': COMMIT, 'call_id': call, 'session': session,
               'run_id': run, 'scenario_id': 'sim-prequote-hit', 'scenario_file_sha256': scenario_sha,
               'prompt_sha256': prompt_sha, 'stored_prompt_preserved': preserved, 'archive': str(source),
               'archive_files_verified': 5, 'audio_seconds': round(seconds, 3), 'sample_rate': rate,
               'channels_with_signal': sum(signal), 'caller_lines': speakers['caller'], 'assistant_lines': speakers['assistant'],
               'cached_fillers': len(current.get('fillers', [])), 'input_on_clock': meta.get('input_on_clock'),
               'input_dropped_ms': meta.get('input_dropped_ms'), 'strict_gate': strict['gate'],
               'first_audio': strict['first_audio'], 'first_content': strict['first_content'],
               'tool_result': strict['tool_result'], 'quote_readout': strict['quote_readout'], 'counts': strict['counts'],
               'receiver_events_truncated': report.get('events_truncated'), 'receiver_transcript_truncated': report.get('transcript_truncated'),
               'receiver_failure_count': len(report.get('failures', [])), 'receiver_warning_count': len(report.get('warnings', [])),
               'receiver_first_audio': {'scope': 'simulator application audio boundary, including openers; not handset/carrier playout',
                                        'valid_n': len(values), 'excluded_n': len(gaps)-len(values),
                                        'p50_ms': quantile(.5), 'p95_ms': quantile(.95), 'max_ms': max(values) if values else None},
               'quotation_exercise': exercise, 'output_accounting': output_accounting, 'handset_target_proven': False}
    (out/'summary.json').write_text(json.dumps(summary, indent=2)+'\n')
    state.update(stage='evidence_collected', session=session, strict_gate_passed=strict['gate']['passed'],
                 quotation_exercise_passed=exercise['passed'])
    save_state()
    print(json.dumps(summary), flush=True)
    require(output_accounting['passed'], 'output accounting remains incomplete or ambiguous; evidence retained, do not redial this attempt')
    require(exercise['passed'], 'targeted quotation exercise incomplete; need two observed readouts and one confirmed nondefault omission; evidence retained, do not redial this attempt')
except Exception as error:
    state.update(stage='needs_review', failure=type(error).__name__)
    save_state()
    print('STOP: '+(str(error) if isinstance(error, RuntimeError) else type(error).__name__), flush=True)
    if run and not completed:
        try:
            sim_request('/sim/v1/runs/'+run+'/stop', 'POST')
            print('requested_stop_for_owned_run='+run, flush=True)
        except RuntimeError:
            print('owned_run_stop_unconfirmed='+run, flush=True)
    raise SystemExit(1)
PY
