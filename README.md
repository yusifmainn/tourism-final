# Aze-Tourism — Rəsmi Korporativ Tur Operatoru

Dövlət qurumları və şirkətlər üçün **Qarabağ Teambuilding** turunun saytı (2 gün 1 gecə,
Bakı → Ağdam → Xankəndi → Şuşa → Füzuli → Bakı): ana səhifə, onlayn rezervasiya və 10 ₼ depozit,
rollu admin panel, sayta giriş loqları. Go backend, PostgreSQL, Docker.

**Serverə quraşdırma və istismar: [DEPLOY.md](DEPLOY.md)**

## Səhifələr

| URL | Səhifə |
|-----|--------|
| `/` | Ana səhifə: tur, marşrut, 2 günlük proqram, qiymət, göstəricilər, FAQ |
| `/rezervasiya/` | Onlayn rezervasiya (3 addım) və 10 ₼ depozit |
| `/sertler/` | İştirak şərtləri və məxfilik |
| `/admin/` | Admin panel: rezervasiyalar və depozit, FAQ, şərtlər, statistika, giriş loqları |

Sayt 3 dildədir (AZ / RU / EN), admin panel Azərbaycan dilindədir.

## Şirkət məlumatları (saytda)

| | |
|---|---|
| Telefon | +994 12 300 00 53 |
| E-poçt | info@aze-tourism.az |
| VÖEN | 1409214732 |
| Lisenziya | № 14-2964 |
| Ünvan | Bakı şəhəri, Mehdi Hüseyn küç., 58 |

## Texnologiyalar

- **Backend:** Go (standart kitabxana və `github.com/lib/pq`)
- **Baza:** PostgreSQL 17
- **Frontend:** HTML, CSS, JavaScript (framework yoxdur), proqramın içinə daxil edilir
- **Deploy:** Docker Compose, Caddy (avtomatik HTTPS)
- **Ödəniş:** Kapital Bank e-commerce API (kod hazırdır, bank girişi gözlənilir)

## Vacib

Onlayn ödəniş hələ qoşulmayıb: ödəniş addımında istənilən kart uğurlu nəticə verir, amma **pul alınmır**.
Depoziti əməkdaşlar sayt xaricində alıb admin paneldə qeyd edir. Tam kart nömrəsi və CVV serverə
göndərilmir; yalnız kartın son 4 rəqəmi və bitmə tarixi saxlanılır və onları yalnız Admin görür.
Ətraflı: DEPLOY.md, 5-ci və 7-ci bölmələr.
