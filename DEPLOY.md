# Aze-Tourism — serverə quraşdırma və istismar

Bu sənəd saytı şirkətin öz serverində (Ubuntu Server 24.04, VMware ESXi üzərində virtual maşın) Docker
ilə qaldıran və sonra ona baxan şəxs üçündür. Addımları sıra ilə edin.

Sayt üç Docker konteynerindən ibarətdir:

| Konteyner | Nədir |
|---|---|
| `db` | PostgreSQL 17. Bütün məlumatlar (rezervasiyalar, vəsiqə şəkilləri, loqlar) serverin diskində, `aze-tourism_db_data` volume-undadır |
| `app` | Saytın özü (Go proqramı): səhifələr, API, admin panel |
| `caddy` | 80/443 portlarını qəbul edir, HTTPS sertifikatını avtomatik alır və sorğuları `app`-a ötürür |

Məlumat heç bir xarici xidmətdə saxlanılmır. Serverdən xaricə yalnız bunlar gedir: Ubuntu və Docker
yeniləmələri, Docker image-ləri, image qurulanda Go modulları və Alpine paketləri, HTTPS sertifikatı
(Let's Encrypt), giriş loqlarında provayderin tapılması üçün DNS sorğuları (9-cu bölmə) və bank
qoşulandan sonra Kapital Bank API-si.

## 1. Server tələbləri

| | |
|---|---|
| ƏS | Ubuntu Server 24.04 LTS, ESXi-də virtual maşın |
| Resurs | 2 vCPU, 4 GB RAM, 40 GB disk (minimum 1 vCPU, 2 GB RAM). Hər gecəlik backup bütün vəsiqə şəkillərini saxlayır, diski ehtiyatla seçin |
| ESXi | VM-in avtomatik açılması yandırılmalıdır: ESXi Host Client → Manage → System → Autostart → Enabled: Yes; sonra bu VM → Actions → Autostart → Enable. Əks halda ESXi yenidən başlayanda (məsələn, elektrik kəsiləndə) sayt sönülü qalır |
| Domen | Məsələn `aze-tourism.az`, `A` qeydi şirkətin xarici IP-sinə |
| Daxil olan portlar | 80 və 443 internetdən (şirkət firewall-unda port yönləndirmə). 22 (SSH) yalnız daxili şəbəkədən |
| Xaricə çıxış | 80 və 443: Ubuntu yeniləmələri (`archive.ubuntu.com`, `security.ubuntu.com`), Docker (`download.docker.com`, `registry-1.docker.io`, `auth.docker.io`, `production.cloudflare.docker.com`), Go modulları (`proxy.golang.org`, `sum.golang.org`), Alpine paketləri (`dl-cdn.alpinelinux.org`), Let's Encrypt (`acme-v02.api.letsencrypt.org`), sonradan Kapital Bank. 53 (DNS), 123/udp (saat). Git yolunda `github.com`-a 22 |

Sayt müştərilər üçündür, ona görə internetdən açılmalıdır. Server şirkətin daxilində qalır, məlumat da
orada saxlanılır. Serverin internetə birbaşa çıxışı yoxdursa (yalnız proxy ilə), bu təlimat olduğu kimi işləmir.

## 2. Quraşdırma

### 2.0. Şəbəkə (şəbəkə administratoru ilə, əvvəlcədən)

1. VM-ə statik daxili IP verin (Ubuntu quraşdırılanda "Network configuration" ekranında).
2. Şirkət firewall-unda xarici IP-nin 80 və 443 TCP portlarını VM-in daxili IP-sinin 80 və 443 portlarına
   yönləndirin. Mənbə IP dəyişdirilməməlidir (SNAT/masquerade olmasın), əks halda giriş loqlarında bütün
   ziyarətçilər bir IP ilə görünür.
3. DNS-də domen üçün `A` qeydi → şirkətin xarici IP-si. Domendə köhnə `AAAA` (IPv6) qeydi varsa, silin.
   `www.` ünvanı da lazımdırsa, onun üçün də `A` qeydi yaradın (2.6).
4. Yoxlayın: `nslookup aze-tourism.az 8.8.8.8` şirkətin xarici IP-sini qaytarmalıdır.

### 2.1. Ubuntu-nu hazırlayın

Ubuntu quraşdırılarkən "Install OpenSSH server"-i seçin, "Featured server snaps" ekranında `docker`-i
**seçməyin** (seçilibsə: `sudo snap remove --purge docker`). Sonra:

```bash
sudo apt update && sudo apt upgrade -y
sudo timedatectl set-timezone Asia/Baku
sudo apt install -y openssh-server open-vm-tools unattended-upgrades curl unzip rsync git
[ -f /var/run/reboot-required ] && sudo reboot
```

Quraşdırıcı LVM ilə diskin yalnız bir hissəsini `/`-a verir. Bütün diski verin (LVM yoxdursa, əmr xəta verir, keçin):

```bash
sudo lvextend -r -l +100%FREE /dev/ubuntu-vg/ubuntu-lv
df -h /
```

Xaricə çıxışı yoxlayın. Hər əmr `HTTP/` ilə başlayan sətir qaytarmalıdır (401 və ya 404 da normaldır):

```bash
curl -sI -m 10 http://archive.ubuntu.com/ubuntu/ | head -1
curl -sI -m 10 https://registry-1.docker.io/v2/ | head -1
curl -sI -m 10 https://proxy.golang.org/ | head -1
curl -sI -m 10 https://dl-cdn.alpinelinux.org/ | head -1
curl -sI -m 10 https://acme-v02.api.letsencrypt.org/directory | head -1
```

Cavab verməyən ünvanı şəbəkə administratorundan açmasını istəyin.

### 2.2. Docker Engine-i quraşdırın

Rəsmi Docker repozitoriyasından (Ubuntu-nun `docker.io` paketi, snap və ya Docker Desktop deyil):

```bash
sudo apt install -y ca-certificates
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Saytı ilk dəfə qaldırmazdan **əvvəl** Docker loqlarının ölçüsünü məhdudlaşdırın, yoxsa loqlar zamanla
diski doldurur. İkinci ayar Docker-in daxili şəbəkələri üçündür. `10.200.0.0/16` şirkətdə istifadə
olunursa, şəbəkə administratorunun verdiyi boş diapazonu yazın:

```bash
sudo tee /etc/docker/daemon.json > /dev/null <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "5" },
  "default-address-pools": [ { "base": "10.200.0.0/16", "size": 24 } ]
}
EOF
sudo systemctl enable docker
sudo systemctl restart docker
sudo docker run --rm hello-world
```

Son əmr "Hello from Docker!" yazmalıdır. Docker server yenidən başlayanda öz-özünə açılır, konteynerlər
də (`restart: unless-stopped`).

### 2.3. Firewall

```bash
sudo ufw allow from 192.168.0.0/16 to any port 22 proto tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

`192.168.0.0/16` əvəzinə şirkətin daxili şəbəkəsini yazın. SSH ilə qoşulduğunuz kompüter bu şəbəkədə
olmalıdır, yoxsa `ufw enable`-dan sonra bağlantı kəsilir.

Docker-in açdığı portlar `ufw` qaydalarından yan keçir. Bu layihədə yalnız Caddy-nin 80 və 443 portları
açılır, baza (5432) və proqram (8080) xaricə açılmır. `docker-compose.yml`-a yeni `ports:` əlavə etməyin.

### 2.4. Layihəni serverə köçürün

Qovluq: `/opt/aze-tourism`. Kompüterdəki `.env` faylı **köçürülmür**: o, yalnız lokal sınaq üçündür,
serverdə yenisi yaradılır (2.5).

**A) Git (tövsiyə olunur, yeniləmək asandır).**

Kompüterdə, bir dəfə: GitHub-da **Private** repozitoriya yaradın (README əlavə etmədən), sonra layihə
qovluğunda (PowerShell):

```powershell
git config --global user.name "Ad Soyad"
git config --global user.email "ad@aze-tourism.az"
git init -b main
git add .
git status
git commit -m "Aze-Tourism"
git remote add origin https://github.com/SIRKET/aze-tourism.git
git push -u origin main
```

`git status` siyahısında `.env` **olmamalıdır** (`.gitignore` onu çıxarır). Sonrakı dəyişikliklər üçün:
`git add .`, `git commit -m "..."`, `git push`.

Serverdə yalnız oxuma hüququ olan açar (deploy key) yaradın:

```bash
sudo install -d -m 700 /root/.ssh
sudo ssh-keygen -t ed25519 -N "" -f /root/.ssh/aze_deploy
sudo cat /root/.ssh/aze_deploy.pub
```

Çıxan sətri GitHub → repozitoriya → Settings → Deploy keys → Add deploy key bölməsinə yapışdırın
("Allow write access" işarələnməsin). Sonra:

```bash
sudo git -c core.sshCommand="ssh -i /root/.ssh/aze_deploy" clone git@github.com:SIRKET/aze-tourism.git /opt/aze-tourism
sudo git -C /opt/aze-tourism config core.sshCommand "ssh -i /root/.ssh/aze_deploy"
```

İlk qoşulmada `yes` yazın. GitHub parolla klonlamağı qəbul etmir.

**B) Zip.**

Kompüterdə:

1. `Aze-Tourism-DEPLOY` qovluğuna daxil olun (qovluğun özünü yox, içindəkiləri zip edəcəksiniz).
   `Ctrl+A` ilə hamısını seçin, `Ctrl` sıxılı halda `.env` faylına klikləyib onu seçimdən çıxarın.
2. Seçilmiş fayllardan birinə sağ klik → "Compress to ZIP file". Zip-in adını `aze-tourism.zip` qoyun və
   onu masaüstünə köçürün (layihə qovluğunda qalmasın).
3. PowerShell-də (`scp` Windows-da var, əlavə proqram lazım deyil):
   ```powershell
   scp $env:USERPROFILE\Desktop\aze-tourism.zip ISTIFADECI@SERVER_IP:~/
   ```

Serverdə:

```bash
cd ~
unzip -l aze-tourism.zip | head -20
sudo mkdir -p /opt/aze-tourism
sudo unzip -q aze-tourism.zip -d /opt/aze-tourism
ls /opt/aze-tourism/docker-compose.yml
```

`unzip -l` siyahısında adlar birbaşa (`Dockerfile`, `docker-compose.yml`, `admin/...`) görünməlidir,
qabağında qovluq adı olmamalıdır. `.env` olmamalıdır, `.env.example` isə olmalıdır. Son əmr faylı
tapmırsa, `sudo rm -rf /opt/aze-tourism` edin və zip-i yenidən hazırlayın.

### 2.5. Tənzimləmələr (`.env`)

```bash
cd /opt/aze-tourism
sudo cp .env.example .env
sudo nano .env
sudo chmod 600 .env
```

Doldurun:

| Dəyişən | Nə yazmaq |
|---|---|
| `DOMAIN` | Saytın domeni, məsələn `aze-tourism.az` |
| `POSTGRES_PASSWORD` | Bazanın parolu |
| `ADMIN_PASSWORD` | `admin` istifadəçisinin parolu. Boş qalsa parol `admin` olur, **boş qoymayın** |
| `FINANCE_PASSWORD` | `maliyye` istifadəçisinin parolu |
| `OPERATOR_PASSWORD` | `isci` istifadəçisinin parolu |
| `KAPITAL_USERNAME`, `KAPITAL_PASSWORD`, `KAPITAL_API_URL` | Hələlik boş (7-ci bölmə) |

Parol yaratmaq üçün: `openssl rand -hex 16`. Yalnız hərf və rəqəmdən ibarət parol yazın.
`DATABASE_URL`, `PORT`, `TRUST_PROXY` sətirlərinə toxunmayın: Docker onları nəzərə almır, onlar
`docker-compose.yml`-da qurulur. `PUBLIC_URL`-u boş saxlayın: boş olanda `https://DOMAIN` götürülür.

### 2.6. HTTPS

- **Domen internetdən açılırsa (normal hal):** 2.0-dakı DNS və port yönləndirmə hazırdırsa, heç nə etmək
  lazım deyil. Caddy ilk açılışda Let's Encrypt sertifikatını özü alır və yeniləyir.
- **Şirkətin öz sertifikatı varsa:** `/opt/aze-tourism/certs/` qovluğuna `cert.pem` (sertifikat və
  aralıq sertifikatlar bir faylda, tam zəncir) və `key.pem` (parolsuz açar) qoyun:
  `sudo chmod 700 /opt/aze-tourism/certs && sudo chmod 600 /opt/aze-tourism/certs/key.pem`.
  `Caddyfile`-da `encode gzip` sətrindən əvvəl `tls /etc/caddy/certs/cert.pem /etc/caddy/certs/key.pem`
  əlavə edin, `docker-compose.yml`-da `caddy` xidmətinin `volumes:` siyahısına
  `- ./certs:/etc/caddy/certs:ro` əlavə edin. `certs` qovluğu Git-ə və Docker image-inə düşmür.
  Git ilə işləyirsinizsə, `Caddyfile` və `docker-compose.yml` dəyişikliyini kompüterdə edib push edin,
  serverdə əl ilə dəyişməyin (növbəti `git pull` dayanar). Sertifikatın müddəti bitməzdən əvvəl
  faylları yeniləyib `sudo docker compose restart caddy` edin.
- **Yalnız daxili sınaq üçün:** `DOMAIN=:80` yazın və saytı `http://SERVER_IP/` ilə açın. Bu rejimdə
  HTTPS yoxdur, real müştəri məlumatı (vəsiqə şəkilləri) üçün yaramır.

`www.` ünvanı da işləməlidirsə (DNS-də `www` qeydi varsa), `Caddyfile`-ın sonuna əlavə edin:

```
www.{$DOMAIN} {
	redir https://{$DOMAIN}{uri} permanent
}
```

### 2.7. Testlər (ilk dəfə, bir dəfə)

```bash
cd /opt/aze-tourism
sudo docker network create aze-test
sudo docker run -d --name aze-test-db --network aze-test -e POSTGRES_PASSWORD=test postgres:17-alpine
sudo docker run --rm --network aze-test -v "$PWD":/src -w /src \
  -e TEST_DATABASE_URL='postgres://postgres:test@aze-test-db:5432/postgres?sslmode=disable' \
  golang:1.27-alpine sh -c 'go vet ./... && go test ./...'
sudo docker rm -f aze-test-db
sudo docker network rm aze-test
```

Nəticə `ok  aze-tourism` olmalıdır. Testlər ayrıca müvəqqəti bazada işləyir, saytın bazasına toxunmur.

### 2.8. Saytı işə salın

```bash
cd /opt/aze-tourism
sudo docker compose up -d --build
sudo docker compose ps
sudo docker compose logs -f app
```

`docker compose ps`-də üç konteyner `Up` olmalıdır. `db` bir neçə saniyəyə, `app` ən çox bir dəqiqəyə
`healthy` olur (əvvəlcə `health: starting` görünə bilər). `caddy`-də health statusu yoxdur. Cədvəllər,
ilkin FAQ, şərtlər və ana səhifə göstəriciləri ilk açılışda avtomatik yaradılır. Loqda:

- `Onlayn ödəniş: söndürülüb (...)`: bank qoşulana qədər normaldır (7-ci bölmə).
- `DİQQƏT: admin parolu standart "admin" dəyərindədir`: görünürsə, `ADMIN_PASSWORD` yazılmayıb.

`Ctrl+C` loqdan çıxır, sayt işləməyə davam edir.

### 2.9. Yoxlayın

Yoxlamanı şirkət şəbəkəsindən **kənardan** edin (məsələn, Wi-Fi-ı söndürülmüş telefonun mobil internetindən).
Çox firewall daxili şəbəkədən şirkətin öz xarici IP-sinə müraciəti (hairpin NAT) dəstəkləmir: ofisdən sayt
açılmaya bilər, açılsa da loqda IP "Lokal şəbəkə" görünər. Ofisdən də açılmalıdırsa, şəbəkə administratoru
hairpin NAT qurmalı və ya daxili DNS-də domeni serverin daxili IP-sinə yönəltməlidir.

- `https://DOMEN/healthz` → `ok`
- `https://DOMEN/`: sayt, loqo, 3 dil
- `https://DOMEN/admin/` → `admin` və `.env`-dəki parol ilə giriş
- Bir test rezervasiyası edin: admin paneldə görünməli, depozit "Alınmayıb" olmalıdır, test kartının son 4 rəqəmi yalnız Admin-ə görünməlidir
- "Giriş loqları" tabında öz girişiniz real IP və provayderlə görünməlidir
- Test rezervasiyasını silin (5.3)

## 3. Gündəlik istismar

Bütün əmrlər `/opt/aze-tourism` qovluğunda işlədilir.

| İş | Əmr |
|---|---|
| Vəziyyət | `sudo docker compose ps` |
| Proqramın loqu | `sudo docker compose logs -f app` |
| HTTPS (Caddy) loqu | `sudo docker compose logs caddy` |
| Yenidən başlatmaq | `sudo docker compose restart app` |
| `.env` dəyişəndən sonra | `sudo docker compose up -d` |
| Dayandırmaq | `sudo docker compose down` |
| Bazaya baxmaq | `sudo docker compose exec db psql -U aze_user -d aze_tourism` |
| Disk yeri (ayda bir) | `df -h /` və `sudo du -sh /var/backups/aze-tourism`. 80%-dən çox doludursa, ESXi-də diski böyüdün |
| Köhnə image-ləri silmək | `sudo docker image prune -f` və `sudo docker builder prune -f` |

> **Diqqət:** `docker compose down -v`, `docker system prune --volumes` və `docker volume prune` bütün
> məlumatları (rezervasiyalar, şəkillər, loqlar) birdəfəlik silir. Bunları işlətməyin.

### 3.1. Backup

Hər gecə saat 02:30-da bazanın tam nüsxəsi alınır, 14 gündən köhnə nüsxələr silinir:

```bash
sudo mkdir -p /var/backups/aze-tourism
sudo chmod 700 /var/backups/aze-tourism
sudo crontab -e
```

Açılan fayla bu sətri əlavə edin (bir sətirdir):

```
30 2 * * * cd /opt/aze-tourism && docker compose exec -T db pg_dump -U aze_user -d aze_tourism -Fc > /var/backups/aze-tourism/yarimciq.tmp && mv /var/backups/aze-tourism/yarimciq.tmp /var/backups/aze-tourism/aze-$(date +\%F).dump && find /var/backups/aze-tourism -name 'aze-*.dump' -mtime +14 -delete
```

Nüsxə yalnız `pg_dump` uğurla bitəndə `aze-TARIX.dump` adını alır. Dərhal bir dəfə əl ilə yoxlayın:

```bash
sudo sh -c 'cd /opt/aze-tourism && docker compose exec -T db pg_dump -U aze_user -d aze_tourism -Fc > /var/backups/aze-tourism/aze-yoxlama.dump'
sudo ls -lh /var/backups/aze-tourism
```

Faylın ölçüsü 0 olmamalıdır, növbəti səhər həmin qovluqda günün tarixli fayl olmalıdır.

Nüsxələri bu VM-dən kənara da köçürün, VM ilə birlikdə itməsinlər. Məsələn, şirkətin NAS/backup
paylaşımını serverə qoşun (`/mnt/nas-backup`) və cron sətrinin sonuna
`&& cp /var/backups/aze-tourism/aze-$(date +\%F).dump /mnt/nas-backup/` əlavə edin. ESXi snapshot-u bazanın
backup-unu əvəz etmir. Backup fayllarında vəsiqə şəkilləri və IP loqları var: paylaşımı yalnız
səlahiyyətli şəxslər aça bilməlidir.

**Bərpa** (`aze-TARIX.dump` əvəzinə real fayl adı; siyahı: `sudo ls -lh /var/backups/aze-tourism`):

```bash
cd /opt/aze-tourism
sudo docker compose stop app
sudo sh -c 'docker compose exec -T db pg_restore -U aze_user -d aze_tourism --clean --if-exists < /var/backups/aze-tourism/aze-TARIX.dump'
sudo docker compose start app
```

`sudo` `<` və `>` yönləndirmələrinə şamil olunmur, ona görə backup və bərpa əmrləri `sudo sh -c '...'`
içində yazılıb. Bərpa vaxtı proqram dayandırılır ki, yarımçıq bazaya yazmasın.

### 3.2. Yeniləmə

**Kod dəyişəndə:**

1. Backup götürün və ESXi-də VM snapshot-u alın:
   ```bash
   cd /opt/aze-tourism
   sudo sh -c 'docker compose exec -T db pg_dump -U aze_user -d aze_tourism -Fc > /var/backups/aze-tourism/aze-evvel-$(date +%F-%H%M).dump'
   sudo ls -lh /var/backups/aze-tourism
   ```
2. Yeni kodu gətirin.
   - Git ilə: `cd /opt/aze-tourism && sudo git pull`
   - Zip ilə: yeni zip-i 2.4-dəki kimi hazırlayıb serverə köçürün, sonra:
     ```bash
     rm -rf ~/aze-yeni && mkdir ~/aze-yeni
     unzip -q ~/aze-tourism.zip -d ~/aze-yeni
     ls ~/aze-yeni/docker-compose.yml
     sudo rsync -a --delete --exclude=.env --exclude=certs/ ~/aze-yeni/ /opt/aze-tourism/
     ```
     `rsync --delete` yeni versiyada olmayan köhnə faylları silir, `.env`-ə və `certs/`-ə toxunmur.
     Zip-i `unzip -o` ilə birbaşa `/opt/aze-tourism`-ə açmayın.
3. `cd /opt/aze-tourism && sudo docker compose up -d --build`
4. 2.9-dakı yoxlamaları təkrarlayın. Hər şey qaydasındadırsa, snapshot-u silin.

Baza cədvəlləri proqram açılanda avtomatik yenilənir, mövcud məlumata toxunulmur.

**Ayda bir dəfə (kod dəyişməsə də)**, backup-dan sonra PostgreSQL, Caddy və Alpine image-lərinin təhlükəsizlik
yeniləmələrini alın:

```bash
cd /opt/aze-tourism
sudo docker compose pull
sudo docker compose build --pull
sudo docker compose up -d
```

## 4. İstifadəçilər və rollar

| Giriş | Rol | Nə görür |
|---|---|---|
| `admin` | Admin | Hər şey: rezervasiyalar, depozit, kartın son 4 rəqəmi və bitmə tarixi, vəsiqə şəkli, FAQ, Şərtlər, Statistika, Giriş loqları |
| `maliyye` | Maliyyə | Rezervasiyalar və depozit (status, əl ilə qeyd). Kart, vəsiqə şəkli, FAQ, Şərtlər, Statistika və loqlar yox |
| `isci` | İşçi | Rezervasiyalar, vəsiqə şəkli, FAQ, Şərtlər, Statistika. Depozit, kart və loqlar yox |

Bu qaydaları server özü yoxlayır, panel yalnız uyğun hissələri göstərir.

**Parolu dəyişmək:** `.env`-də yeni dəyəri yazın və `sudo docker compose up -d`. Həmin istifadəçinin açıq
girişləri bağlanır. Paneldən parol dəyişmək mümkün deyil.

**Admin paneldə dəyişilən məzmun** (kod dəyişmədən):

| Bölmə | Nə dəyişir |
|---|---|
| FAQ idarəsi | Ana səhifədəki suallar (3 dil) |
| Şərtlər | İştirak şərtləri və məxfilik səhifəsi (3 dil) |
| Statistika | Ana səhifədəki 4 göstərici (`12+` il və s.). Rəqəm boş qalsa, göstərici gizlənir |

**Əlavə qoruma (istəyə bağlı).** Əməkdaşlar admin panelə yalnız ofisdən girirsə, panel internetdən
bağlana bilər. `Caddyfile`-ı belə dəyişin (`OFIS_XARICI_IP` əvəzinə şirkətin xarici IP-si), sonra
`sudo docker compose restart caddy`:

```
{$DOMAIN} {
	encode gzip
	@admin_kenar {
		path /admin /admin/* /api/admin/*
		not remote_ip 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 OFIS_XARICI_IP
	}
	respond @admin_kenar 403
	reverse_proxy app:8080
}
```

## 5. Rezervasiya və depozit

### 5.1. Hazırkı vəziyyət: onlayn ödəniş qoşulmayıb

- Müştəri 3 addımda rezervasiya edir: şəxsi məlumatlar və tarix, vəsiqə şəkli və şərtlərin qəbulu, ödəniş
  addımı (hesab ünvanı və kart xanaları). Ödəniş addımında yalnız **10 ₼ depozit** göstərilir.
- Düzgün formatda istənilən kart "Rezervasiyanız uğurla tamamlandı" ekranına və referans nömrəsinə
  (`AZT-XXXXXX-İL`) aparır. **Kartdan pul çəkilmir.**
- Serverə yalnız kartın **son 4 rəqəmi və bitmə tarixi** gedir, onları yalnız Admin görür. Tam kart
  nömrəsi və CVV serverə göndərilmir və heç yerdə saxlanılmır.
- Admin paneldə depozit **"Alınmayıb"** görünür. Müştəri depoziti ödədiyini düşünə bilər: əməkdaş onunla
  əlaqə saxlayıb depoziti sayt xaricində (nağd və ya köçürmə ilə) almalı və rezervasiya detalında
  **"Depoziti ödənilmiş kimi qeyd et"** düyməsi ilə qeyd etməlidir. Səhv qeyd "Əl ilə qeydi ləğv et" ilə geri alınır.
- Sayt avtomatik e-poçt göndərmir. Təsdiq məktubunu əməkdaşlar özləri göndərir.

### 5.2. Depozit məbləği

Məbləğ `payment.go`-da `depositAmount = 10` sabitidir. "10 ₼" həmçinin mətnlərdə yazılıb: `index.html`,
`rezervasiya/index.html`, `assets/js/i18n.js` (`card.note`, `pricebox.fine`, 3 dil), FAQ və şərtlər
(admin panel). Məbləği dəyişsəniz, hamısını birlikdə dəyişin.

### 5.3. Rezervasiyanı silmək

```bash
sudo docker compose exec db psql -U aze_user -d aze_tourism -c "DELETE FROM bookings WHERE ref = 'AZT-...';"
```

Rezervasiya ilə birlikdə vəsiqə şəkli və bank sifarişləri də silinir. Rezervasiyalar avtomatik silinmir.

## 6. Kodda olan məlumatlar

Bunları dəyişmək üçün faylları kompüterdə dəyişib 3.2-dəki kimi yeniləyin.

| Nə | Harada |
|---|---|
| Tur qiyməti (160 ₼) | `index.html` (hero kartı və qiymət bloku), həmçinin FAQ-dakı qiymət sualı (admin panel, 3 dil) |
| Proqram, marşrut, maraqlı yerlər | `index.html` və `assets/js/i18n.js` (3 dil) |
| Telefon, VÖEN, lisenziya, ünvan | `index.html`, `rezervasiya/index.html`, `sertler/index.html`, `assets/js/i18n.js` (`legal.*`) |
| Loqo | `assets/img/logo.png` (1242×240), favicon: `favicon.ico`, `assets/img/favicon-*.png`, `assets/img/apple-touch-icon.png` |
| İlkin FAQ, şərtlər, göstəricilər | `seed.json`. Yalnız **boş bazada** ilk açılışda yazılır, sonra admin paneldən dəyişilir |

## 7. Kapital Bank-ı qoşmaq (API alınandan sonra)

Kod hazırdır, amma bankın öz mühitində yoxlanılmayıb. Bank girişi `.env`-ə yazılanda kart xanaları
saytdan öz-özünə gizlənir, müştəri kartı bankın təhlükəsiz ödəniş səhifəsində yazır. Bank müştərini
`https://DOMEN/odenis/netice` ünvanına qaytarır, server ödənişi bankın API-sindən yoxlayıb depoziti
"Ödənilib" qeyd edir. Bankın API-si haqqında bütün ehtimallar `kapital.go`-nun başında yazılıb.

1. Bankdan **test** girişini alın. `.env`-də yazın:
   ```
   KAPITAL_API_URL=https://txpgtst.kapitalbank.az/api
   KAPITAL_USERNAME=bankın verdiyi test logini
   KAPITAL_PASSWORD=bankın verdiyi test parolu
   ```
2. `sudo docker compose up -d`. Loqda `Onlayn ödəniş: aktivdir (...)` görünməlidir.
3. Bankın test kartı ilə yoxlayın:
   - ödəniş: sayt "Depozit ödənildi" göstərir, admin paneldə depozit "Ödənilib", metod "Kapital Bank";
   - imtina: sayt "Ödəniş tamamlanmadı" göstərir;
   - ödəyib bankın səhifəsini bağlamaq: ən çox 5 dəqiqə sonra depozit "Ödənilib" olur.
4. Test rezervasiyalarını silin (5.3).
5. Real giriş: `KAPITAL_API_URL` sətrini boş qoyun, real `KAPITAL_USERNAME` və `KAPITAL_PASSWORD` yazın,
   `sudo docker compose up -d`. Öz kartınızla 10 ₼ ödəyib yoxlayın.

Bankdan soruşun: sifariş növü `Order_SMS` (birbaşa ödəniş) uyğundurmu, API üçün serverin IP-sini qeydiyyata
almaq lazımdırmı. Bilinməli məhdudiyyətlər:

- Bankın sonradan qaytardığı ödəniş saytda avtomatik görünmür.
- 48 saatdan köhnə bitməmiş sifarişlər yoxlanılmır.
- Eyni depozit iki dəfə ödənilsə, loqa `DİQQƏT: ... ikinci dəfə ödənildi` yazılır. Artıq məbləği bank vasitəsilə qaytarın.

## 8. Tənzimləmələrin tam siyahısı

| Dəyişən | Mənası | Default |
|---|---|---|
| `DOMAIN` | Saytın domeni (Caddy və bankın qayıdış ünvanı üçün) | məcburi |
| `POSTGRES_PASSWORD` | Bazanın parolu. Yalnız bazanın ilk yaradılmasında tətbiq olunur | məcburi |
| `ADMIN_PASSWORD` | `admin` parolu | `admin` |
| `FINANCE_PASSWORD` | `maliyye` parolu | məcburi |
| `OPERATOR_PASSWORD` | `isci` parolu | məcburi |
| `KAPITAL_USERNAME`, `KAPITAL_PASSWORD` | Kapital Bank terminal girişi. Boşdursa onlayn ödəniş söndürülüb | boş |
| `KAPITAL_API_URL` | Bankın API ünvanı | `https://e-commerce.kapitalbank.az/api` |
| `PUBLIC_URL` | Saytın tam ünvanı | boş: `https://DOMAIN` |

`POSTGRES_PASSWORD`-u baza yaradılandan sonra dəyişsəniz, bazada da dəyişin:
`sudo docker compose exec db psql -U aze_user -d aze_tourism -c "ALTER USER aze_user WITH PASSWORD 'yeni';"`.

## 9. Giriş loqları

Admin paneldə **"Giriş loqları"** tabı yalnız Admin-ə görünür. Qeyd olunur:

- saytın səhifələrinə hər daxil olma;
- admin panelə hər giriş cəhdi, uğurlu və uğursuz, yazılan istifadəçi adı ilə (parol yazılmır).

Hər qeyddə: vaxt, IP, provayder və ASN, şəbəkənin ölkəsi, IP-nin DNS adı, səhifə, cihazın növü və modeli,
əməliyyat sistemi, brauzer, ekran, dil, saat qurşağı, avadanlıq və User-Agent. Qeydlər **180 gün**
saxlanılır, sonra avtomatik silinir. Bu barədə şərtlərin "Məxfilik" bölməsində yazılıb.

Provayder Team Cymru-nun pulsuz DNS xidmətindən tapılır (açar lazım deyil). Bunun üçün serverin DNS
serveri internet adlarını həll edə bilməlidir. Bacarmırsa, qeyd yenə yazılır, amma provayder boş qalır.
Bu sorğularda ziyarətçinin IP ünvanı DNS vasitəsilə Team Cymru-ya gedir.

## 10. Real müştərilərdən əvvəl

- [ ] HTTPS işləyir (`https://DOMEN` brauzerdə kilid ilə açılır)
- [ ] `ADMIN_PASSWORD`, `FINANCE_PASSWORD`, `OPERATOR_PASSWORD` güclü parollardır, loqda "standart admin" xəbərdarlığı yoxdur
- [ ] Gecəlik backup işləyir, nüsxələr VM-dən kənara da köçürülür, bərpa bir dəfə sınanıb
- [ ] ESXi-də VM-in avtomatik açılması yandırılıb
- [ ] Ana səhifədəki göstəricilər (`12+`, `3 500+`, `60+`, `4.9/5`) realdır, real deyilsə admin paneldə "Statistika"-dan düzəldilib
- [ ] `info@aze-tourism.az` qutusu mövcuddur və oxunur
- [ ] Saytda yazılan xidmətlər (4 ulduzlu otel, sığorta, 24/7 dəstək, rəsmi bələdçi) rəhbərlik tərəfindən təsdiqlənib
- [ ] Əməkdaşlar depozitin hazırda onlayn alınmadığını bilir və onu əl ilə qeyd edir (5.1)
- [ ] Test rezervasiyaları silinib

## 11. Hələ edilməyənlər

| Modul | Vəziyyət |
|---|---|
| Onlayn ödəniş | Kod hazırdır, Kapital Bank girişi gözlənilir (7-ci bölmə) |
| E-poçt və SMS | Yoxdur, təsdiq məktubu əl ilə göndərilir |
| Vəsiqə şəkillərinin şifrələnməsi | Şəkillər bazada açıq saxlanılır, yalnız Admin və İşçi görür |
| Rezervasiyaların avtomatik silinməsi | Yoxdur (loqlar 180 gündən sonra silinir) |
| Tarix üzrə yer limiti | Yoxdur |
| Paneldən parol dəyişmə | Yoxdur, `.env` ilə dəyişilir |

## 12. Problemlərin həlli

| Problem | Həll |
|---|---|
| `docker compose up` `.env faylinda ... yazin` xətası verir | `.env`-də həmin dəyişən boşdur (2.5) |
| `docker compose`: `no configuration file provided` | Əmri `/opt/aze-tourism`-da işlədin; zip əlavə alt qovluqla açılıbsa, 2.4-ə baxın |
| HTTPS sertifikatı alınmır | Domen bu serverin xarici IP-sinə yönəlməli, 80 və 443 internetdən açıq olmalı, köhnə `AAAA` qeydi olmamalıdır. `sudo docker compose logs caddy` |
| `/healthz` 503 qaytarır | Proqram işləyir, baza əlçatan deyil: `sudo docker compose logs db` |
| `password authentication failed` | `POSTGRES_PASSWORD` baza yaradılandan sonra dəyişdirilib (8-ci bölmə) |
| Saytda `Server xətası` | Səbəb proqramın loqundadır: `sudo docker compose logs app` |
| Giriş loqlarında provayder boşdur | Serverin DNS serveri internet adlarını həll etmir. Yoxlayın: `resolvectl query -t TXT 8.8.8.8.origin.asn.cymru.com` (cavabda `15169` olmalıdır) |
| Kənardan (mobil internetdən) girişlər də daxili IP ilə ("Lokal şəbəkə") görünür | Şirkətin firewall-u ziyarətçinin IP-sini öz IP-si ilə əvəz edir (SNAT). Port yönləndirmə mənbə IP-ni dəyişməməlidir. Yalnız ofisdən edilən girişlərin belə görünməsi normaldır (2.9) |
| Giriş loqları tabı görünmür | Tab yalnız Admin-ə görünür |
| Bank qoşulub, amma loqda `Onlayn ödəniş: söndürülüb` | Sətirdə hansı dəyişənin çatmadığı yazılır. `.env` dəyişəndən sonra `sudo docker compose up -d` |
| Loqda `Kapital Bank: HTTP 401` və ya `403` | Login/parol yanlışdır və ya test girişi real ünvanla işlədilir (və əksinə): `KAPITAL_API_URL`-i yoxlayın |
| Bankdan qayıdanda `?odenis=xeta` | Bankın qaytardığı sifariş bu serverin bazasında tapılmadı: test və real mühit qarışıb və ya baza sifarişdən əvvəlki backup-dan bərpa olunub. `sudo docker compose logs app` |

## 13. API (developer üçün)

| Metod və yol | İcazə | Təsvir |
|---|---|---|
| `GET /healthz` | ictimai | Proqram və baza işləyir |
| `GET /api/config` | ictimai | Depozit (`10`), valyuta və onlayn ödənişin aktiv olub-olmadığı |
| `GET /api/faq`, `/api/terms`, `/api/stats` | ictimai | FAQ, şərtlər, ana səhifə göstəriciləri |
| `POST /api/bookings` | ictimai | Yeni rezervasiya (IP üzrə saatda 60). Bank qoşulmayanda yalnız `cardLast4` və `cardExp` qəbul edir |
| `POST /api/bookings/{ref}/pay` | ictimai | Yeni bank sifarişi (bank qoşulanda) |
| `GET /api/bookings/{ref}/payment` | ictimai | Depozitin statusu, şəxsi məlumat yoxdur |
| `GET /odenis/netice` | ictimai | Bankdan qayıdış |
| `POST /api/visit` | ictimai | Səhifə qeydi (giriş loqları) |
| `POST /api/admin/login`, `/logout`, `GET /api/admin/me` | ictimai / giriş | Giriş və çıxış (IP üzrə 15 dəqiqədə 10 uğursuz cəhd) |
| `GET /api/admin/bookings`, `/bookings/{ref}` | giriş | Siyahı və detal, rola görə süzülür (kart yalnız Admin-ə) |
| `PATCH /api/admin/bookings/{ref}` | giriş | `status` (hər rol), `paymentStatus` (Admin, Maliyyə) |
| `GET /api/admin/bookings/{ref}/document` | Admin, İşçi | Vəsiqə şəkli |
| `GET /api/admin/visits` | Admin | Giriş loqları və statistika |
| `POST`, `PUT`, `DELETE /api/admin/faq…`, `PUT /api/admin/terms`, `PUT /api/admin/stats` | Admin, İşçi | Məzmunun redaktəsi |
