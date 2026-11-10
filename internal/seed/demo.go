package seed

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/search"
	"github.com/shtmthw/ponnoKhoj/internal/store"
	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// DemoProduct is one entry of the demo catalog. Every demo product is
// generic (no brand) and every demo price is invented for demonstration:
// BaseTaka is NOT market data.
type DemoProduct struct {
	NameEn   string
	NameBn   string
	Category string
	Size     string
	BaseTaka int64
	Aliases  []string
	// Coverage selects the demo scenario this product illustrates.
	Coverage coverage
}

type coverage int

const (
	coverEverywhere coverage = iota // listed at most shops: the normal local search
	coverFarOnly                    // listed only in the far cluster: nothing within 3 km, estimate from wider rings
	coverNowhere                    // no local listings at all: online only, estimate is insufficient_data
)

// DemoCatalog is the demo product catalog.
var DemoCatalog = []DemoProduct{
	{"Miniket Rice 5 kg", "মিনিকেট চাল ৫ কেজি", "rice-grains", "5 kg", 400, []string{"চাউল", "chaul", "rice", "bhat er chal"}, coverEverywhere},
	{"Miniket Rice 1 kg", "মিনিকেট চাল ১ কেজি", "rice-grains", "1 kg", 82, []string{"চাউল", "chaul", "rice"}, coverEverywhere},
	{"Nazirshail Rice 5 kg", "নাজিরশাইল চাল ৫ কেজি", "rice-grains", "5 kg", 450, []string{"nazir", "najirshail", "চাউল", "rice"}, coverFarOnly},
	{"Chinigura Rice 1 kg", "চিনিগুঁড়া চাল ১ কেজি", "rice-grains", "1 kg", 150, []string{"polao rice", "পোলাওয়ের চাল", "chinigura", "aromatic rice"}, coverNowhere},
	{"Atta (Wheat Flour) 2 kg", "আটা ২ কেজি", "rice-grains", "2 kg", 130, []string{"flour", "ata"}, coverEverywhere},
	{"Red Lentil (Masoor Dal) 1 kg", "মসুর ডাল ১ কেজি", "lentils-pulses", "1 kg", 140, []string{"daal", "lentil", "moshur", "musur dal"}, coverEverywhere},
	{"Mung Dal 1 kg", "মুগ ডাল ১ কেজি", "lentils-pulses", "1 kg", 180, []string{"moong", "mug dal", "daal"}, coverEverywhere},
	{"Chickpea (Chola) 1 kg", "ছোলা ১ কেজি", "lentils-pulses", "1 kg", 110, []string{"boot", "বুট", "chana"}, coverEverywhere},
	{"Soybean Oil 1 L", "সয়াবিন তেল ১ লিটার", "cooking-oil", "1 l", 175, []string{"tel", "oil", "soyabean", "soya oil"}, coverEverywhere},
	{"Soybean Oil 5 L", "সয়াবিন তেল ৫ লিটার", "cooking-oil", "5 l", 850, []string{"tel", "oil", "soyabean", "soya oil"}, coverEverywhere},
	{"Mustard Oil 1 L", "সরিষার তেল ১ লিটার", "cooking-oil", "1 l", 280, []string{"shorisha", "sorisha tel", "oil"}, coverEverywhere},
	{"Sugar 1 kg", "চিনি ১ কেজি", "spices-salt-sugar", "1 kg", 135, []string{"cini"}, coverEverywhere},
	{"Iodized Salt 1 kg", "আয়োডিনযুক্ত লবণ ১ কেজি", "spices-salt-sugar", "1 kg", 42, []string{"lobon", "nun", "নুন", "salt"}, coverEverywhere},
	{"Turmeric Powder 200 g", "হলুদ গুঁড়া ২০০ গ্রাম", "spices-salt-sugar", "200 g", 110, []string{"holud", "halud", "haldi"}, coverEverywhere},
	{"Chili Powder 200 g", "মরিচ গুঁড়া ২০০ গ্রাম", "spices-salt-sugar", "200 g", 120, []string{"morich", "chilli", "mirchi"}, coverEverywhere},
	{"Farm Egg 12 pcs", "ফার্মের ডিম ১২ টি", "eggs-dairy", "12 pc", 150, []string{"dim", "anda", "eggs", "ডিম ১ ডজন"}, coverEverywhere},
	{"Liquid Milk 1 L", "তরল দুধ ১ লিটার", "eggs-dairy", "1 l", 95, []string{"dudh", "doodh"}, coverEverywhere},
	{"Powder Milk 500 g", "গুঁড়া দুধ ৫০০ গ্রাম", "eggs-dairy", "500 g", 430, []string{"gura dudh", "milk powder"}, coverEverywhere},
	{"Onion (Local) 1 kg", "দেশি পেঁয়াজ ১ কেজি", "vegetables", "1 kg", 80, []string{"peyaj", "piaz", "piyaj", "deshi peyaj"}, coverEverywhere},
	{"Potato 1 kg", "আলু ১ কেজি", "vegetables", "1 kg", 30, []string{"aloo", "alu"}, coverEverywhere},
	{"Garlic 500 g", "রসুন ৫০০ গ্রাম", "vegetables", "500 g", 110, []string{"roshun", "rosun"}, coverEverywhere},
	{"Ginger 500 g", "আদা ৫০০ গ্রাম", "vegetables", "500 g", 120, []string{"ada"}, coverEverywhere},
	{"Tea 400 g", "চা পাতা ৪০০ গ্রাম", "beverages", "400 g", 220, []string{"cha", "cha pata", "tea leaf"}, coverEverywhere},
	{"Drinking Water 2 L", "খাবার পানি ২ লিটার", "beverages", "2 l", 35, []string{"pani", "water bottle", "mineral water"}, coverEverywhere},
	{"Bath Soap 100 g", "গোসলের সাবান ১০০ গ্রাম", "personal-care", "100 g", 45, []string{"saban", "shaban"}, coverEverywhere},
	{"Toothpaste 100 g", "টুথপেস্ট ১০০ গ্রাম", "personal-care", "100 g", 85, []string{"paste", "পেস্ট", "monjon"}, coverEverywhere},
	{"Shampoo 180 ml", "শ্যাম্পু ১৮০ মিলি", "personal-care", "180 ml", 190, []string{"sampu"}, coverEverywhere},
	{"LED Bulb 12W", "এলইডি বাল্ব ১২ ওয়াট", "electronics", "1 pc", 220, []string{"light", "bati", "বাতি", "energy bulb"}, coverEverywhere},
	{"Detergent Powder 1 kg", "ডিটারজেন্ট পাউডার ১ কেজি", "home-care", "1 kg", 140, []string{"washing powder", "কাপড় কাচার পাউডার"}, coverEverywhere},
	{"Dishwashing Liquid 500 ml", "ডিশ ওয়াশিং লিকুইড ৫০০ মিলি", "home-care", "500 ml", 110, []string{"dish wash", "bason majar liquid"}, coverEverywhere},
}

// demoCity places demo shops relative to a district centroid from the seed
// dataset. The offsets are synthetic: demo shops are fictional and their
// positions are not real addresses.
type demoCity struct {
	District    string  // district name_en in the areas dataset
	Label       string  // short label used in shop names
	PriceFactor float64 // makes the two cities differ slightly
	// FarUpazila is the upazila the far cluster's shops are registered in,
	// so upazila-scoped search has demo data. The dataset has no upazila
	// coordinates, so the cluster's (synthetic) positions are not claimed
	// to lie inside the upazila's real boundary.
	FarUpazila string
}

var demoCities = []demoCity{
	{District: "Chattogram", Label: "CTG", PriceFactor: 1.00, FarUpazila: "Hathazari"},
	{District: "Dhaka", Label: "DHK", PriceFactor: 1.04, FarUpazila: "Savar"},
}

// demoShopDescription is what a demo vendor wrote about their shop.
const demoShopDescription = "Fictional demo grocery and household shop: rice, lentils, cooking oil, spices, eggs, vegetables, toiletries and cleaning supplies. ডেমো মুদি দোকান — বাস্তব নয়।"

// demoClusters are groups of shops at an offset from the district centroid.
// The third cluster is ~8.5 km away: outside a 3 km search, inside a 10 km one.
var demoClusters = []struct {
	dLat, dLng float64
	shops      int
	far        bool
}{
	{0, 0, 5, false},
	{0.018, 0.012, 4, false},
	{0.075, -0.010, 4, true},
}

// DemoOptions configures the demo seed.
type DemoOptions struct {
	// AdminPassword and VendorPassword are the passwords of the demo
	// accounts. They are supplied by the operator (never hardcoded).
	AdminPassword  string
	VendorPassword string
	Now            time.Time
}

// Demo account emails. ".test" is a reserved TLD: these are not real mailboxes.
const (
	DemoAdminEmail  = "admin@ponnokhoj.test"
	DemoVendorEmail = "vendor@ponnokhoj.test"
)

// DemoSummary reports what the demo seed created or refreshed.
type DemoSummary struct {
	Products int
	Shops    int
	Listings int
}

// Demo loads the demo catalog, accounts, shops and listings. It is
// idempotent, deterministic, and everything it creates is flagged is_demo.
func Demo(ctx context.Context, st *store.Store, authSvc *auth.Service, opts DemoOptions) (DemoSummary, error) {
	var sum DemoSummary
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	cats, err := Categories(ctx, st)
	if err != nil {
		return sum, err
	}

	admin, err := ensureUser(ctx, st, authSvc, DemoAdminEmail, opts.AdminPassword, "Demo Admin", domain.RoleAdmin)
	if err != nil {
		return sum, err
	}
	vendors := make([]int64, 0, 3)
	for i, email := range []string{DemoVendorEmail, "vendor2@ponnokhoj.test", "vendor3@ponnokhoj.test"} {
		id, err := ensureUser(ctx, st, authSvc, email, opts.VendorPassword, fmt.Sprintf("Demo Vendor %d", i+1), domain.RoleVendor)
		if err != nil {
			return sum, err
		}
		vendors = append(vendors, id)
	}
	_ = admin

	// Products.
	productIDs := make([]int64, len(DemoCatalog))
	for i, p := range DemoCatalog {
		qty := search.ParseQuery(p.Size).Quantity
		if qty == nil {
			return sum, fmt.Errorf("seed: demo product %q has unparsable size %q", p.NameEn, p.Size)
		}
		catID, ok := cats[p.Category]
		if !ok {
			return sum, fmt.Errorf("seed: demo product %q has unknown category %q", p.NameEn, p.Category)
		}
		id, err := st.FindProductByIdentity(ctx, db.FindProductByIdentityParams{
			NameEn: p.NameEn, Brand: "", Model: "", Unit: string(qty.Unit), Quantity: qty.Value,
		})
		if store.IsNotFound(err) {
			id, err = st.InsertProduct(ctx, db.InsertProductParams{
				CategoryID: catID, NameEn: p.NameEn, NameBn: p.NameBn, Unit: string(qty.Unit), Quantity: qty.Value,
				SearchKey: search.ProductKey(p.NameEn, p.NameBn, "", "", qty), Status: "active", IsDemo: true,
			})
		}
		if err != nil {
			return sum, fmt.Errorf("seed: demo product %q: %w", p.NameEn, err)
		}
		for _, alias := range p.Aliases {
			key := search.AliasKey(alias)
			if key == "" {
				continue
			}
			if err := st.InsertProductAlias(ctx, db.InsertProductAliasParams{ProductID: id, Alias: alias, AliasKey: key}); err != nil {
				return sum, fmt.Errorf("seed: alias %q: %w", alias, err)
			}
		}
		productIDs[i] = id
		sum.Products++
	}

	// Shops and listings. A fixed seed keeps the demo data reproducible.
	rng := rand.New(rand.NewPCG(20261110, 4242)) //nolint:gosec // deterministic demo data, not security
	shopNo := 0
	for _, city := range demoCities {
		districtID, err := st.GetAreaByName(ctx, db.GetAreaByNameParams{Kind: "district", NameEn: city.District})
		if err != nil {
			return sum, fmt.Errorf("seed: district %q not found (run the area seed first): %w", city.District, err)
		}
		district, err := st.GetArea(ctx, districtID)
		if err != nil || !district.HasCentroid {
			return sum, errors.Join(fmt.Errorf("seed: district %q has no centroid in the dataset", city.District), err)
		}
		farUpazilaID := district.ID
		if city.FarUpazila != "" {
			upazilas, err := st.ListUpazilasByDistrict(ctx, &district.ID)
			if err != nil {
				return sum, fmt.Errorf("seed: upazilas of %s: %w", city.District, err)
			}
			found := false
			for _, u := range upazilas {
				if u.NameEn == city.FarUpazila {
					farUpazilaID, found = u.ID, true
				}
			}
			if !found {
				return sum, fmt.Errorf("seed: upazila %q not found in %s", city.FarUpazila, city.District)
			}
		}
		for ci, cluster := range demoClusters {
			areaID := district.ID
			if cluster.far {
				areaID = farUpazilaID
			}
			for si := range cluster.shops {
				shopNo++
				lat := district.Lat + cluster.dLat + (rng.Float64()-0.5)*0.016
				lng := district.Lng + cluster.dLng + (rng.Float64()-0.5)*0.016
				name := fmt.Sprintf("Demo Store %s-%d%d (ডেমো দোকান)", city.Label, ci+1, si+1)
				owner := vendors[(shopNo-1)%len(vendors)]
				if city.Label == "CTG" && ci == 0 && si < 3 {
					owner = vendors[0] // the login-able demo vendor owns three central shops
				}
				status := domain.ShopVerified
				if city.Label == "CTG" && ci == 1 && si == 3 {
					status = domain.ShopPending // one shop waiting in the admin verification queue
				}
				shopID, err := ensureShop(ctx, st, owner, name, areaID, lat, lng, status,
					fmt.Sprintf("Demo address %d (fictional)", si+1))
				if err != nil {
					return sum, err
				}
				sum.Shops++
				shopFactor := 0.95 + rng.Float64()*0.11
				for pi, p := range DemoCatalog {
					listed := rng.Float64() < 0.72
					noise := 0.97 + rng.Float64()*0.06
					ageRoll, ageFrac, histRoll := rng.Float64(), rng.Float64(), rng.Float64()
					switch p.Coverage {
					case coverNowhere:
						continue
					case coverFarOnly:
						if !cluster.far {
							continue
						}
						listed = true
					}
					if !listed {
						continue
					}
					price := int64(math.Round(float64(p.BaseTaka)*city.PriceFactor*shopFactor*noise)) * domain.PoishaPerTaka
					flagged := false
					if city.Label == "CTG" && ci == 0 && si == 0 && p.NameEn == "Potato 1 kg" {
						price *= 4 // a deliberately wrong price to show the anomaly review queue
						flagged = true
					}
					var age time.Duration
					switch {
					case ageRoll < 0.80:
						age = time.Duration(ageFrac * 6 * 24 * float64(time.Hour)) // fresh
					case ageRoll < 0.95:
						age = time.Duration((8 + ageFrac*17) * 24 * float64(time.Hour)) // stale
					default:
						age = time.Duration((35 + ageFrac*15) * 24 * float64(time.Hour)) // hidden
					}
					qtyUnit := search.ParseQuery(p.Size).Quantity.Unit
					if err := ensureListing(ctx, st, shopID, productIDs[pi], price, string(qtyUnit), flagged, opts.Now.Add(-age), histRoll); err != nil {
						return sum, err
					}
					sum.Listings++
				}
			}
		}
	}
	return sum, nil
}

func ensureUser(ctx context.Context, st *store.Store, authSvc *auth.Service, email, password, name string, role domain.Role) (int64, error) {
	u, err := authSvc.CreateUser(ctx, email, password, name, role, true)
	if err == nil {
		return u.ID, nil
	}
	if !errors.Is(err, auth.ErrEmailTaken) {
		return 0, fmt.Errorf("seed: demo user %s: %w", email, err)
	}
	// Already there: keep the account, refresh its password and role so the
	// operator-supplied credentials always work after a re-seed.
	row, err := st.GetUserByEmail(ctx, email)
	if err != nil {
		return 0, fmt.Errorf("seed: load demo user %s: %w", email, err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return 0, err
	}
	if err := st.SetUserPassword(ctx, db.SetUserPasswordParams{ID: row.ID, PasswordHash: hash}); err != nil {
		return 0, fmt.Errorf("seed: reset demo password: %w", err)
	}
	if err := st.SetUserRole(ctx, db.SetUserRoleParams{ID: row.ID, Role: string(role)}); err != nil {
		return 0, fmt.Errorf("seed: reset demo role: %w", err)
	}
	if err := st.ResetLoginFailures(ctx, row.ID); err != nil {
		return 0, fmt.Errorf("seed: reset demo lockout: %w", err)
	}
	return row.ID, nil
}

func ensureShop(ctx context.Context, st *store.Store, owner int64, name string, areaID int64, lat, lng float64, status domain.ShopStatus, address string) (int64, error) {
	id, err := st.FindShopByOwnerAndName(ctx, db.FindShopByOwnerAndNameParams{OwnerID: owner, Name: name})
	if err == nil {
		// Re-seeding refreshes the demo shop's details (area, description).
		if _, err := st.UpdateShop(ctx, db.UpdateShopParams{
			ID: id, OwnerID: owner, Name: name, Description: demoShopDescription, Address: address, AreaID: areaID,
			Lat: lat, Lng: lng, Phone: "", OpeningHours: "9:00–21:00 (demo)",
		}); err != nil {
			return 0, fmt.Errorf("seed: update demo shop %q: %w", name, err)
		}
		return id, nil
	}
	if !store.IsNotFound(err) {
		return 0, fmt.Errorf("seed: find demo shop: %w", err)
	}
	id, err = st.CreateShop(ctx, db.CreateShopParams{
		OwnerID: owner, Name: name, Description: demoShopDescription, Address: address, AreaID: areaID, Lat: lat, Lng: lng,
		Phone: "", OpeningHours: "9:00–21:00 (demo)", Status: string(status), IsDemo: true,
	})
	if err != nil {
		return 0, fmt.Errorf("seed: create demo shop %q: %w", name, err)
	}
	return id, nil
}

func ensureListing(ctx context.Context, st *store.Store, shopID, productID, price int64, unit string, flagged bool, updatedAt time.Time, histRoll float64) error {
	return st.InTx(ctx, func(q *db.Queries) error {
		_, err := q.GetListingByShopProduct(ctx, db.GetListingByShopProductParams{ShopID: shopID, ProductID: productID})
		isNew := store.IsNotFound(err)
		if err != nil && !isNew {
			return fmt.Errorf("seed: load demo listing: %w", err)
		}
		id, err := q.UpsertListing(ctx, db.UpsertListingParams{
			ShopID: shopID, ProductID: productID, PricePoisha: price, Unit: unit, InStock: true, AnomalyFlagged: flagged,
		})
		if err != nil {
			return fmt.Errorf("seed: demo listing: %w", err)
		}
		if err := q.SetListingUpdatedAt(ctx, db.SetListingUpdatedAtParams{ID: id, UpdatedAt: updatedAt}); err != nil {
			return fmt.Errorf("seed: backdate demo listing: %w", err)
		}
		if !isNew {
			return nil // history was written the first time
		}
		if err := q.InsertPriceHistory(ctx, db.InsertPriceHistoryParams{
			ListingID: id, ShopID: shopID, ProductID: productID, PricePoisha: price, RecordedAt: &updatedAt,
		}); err != nil {
			return fmt.Errorf("seed: demo price history: %w", err)
		}
		if histRoll < 0.3 && !flagged {
			// An earlier, slightly different price: gives estimates some history.
			old := int64(math.Round(float64(price/domain.PoishaPerTaka)*(0.94+histRoll*0.2))) * domain.PoishaPerTaka
			at := updatedAt.Add(-time.Duration(20+histRoll*60) * 24 * time.Hour)
			if old > 0 && old != price {
				if err := q.InsertPriceHistory(ctx, db.InsertPriceHistoryParams{
					ListingID: id, ShopID: shopID, ProductID: productID, PricePoisha: old, RecordedAt: &at,
				}); err != nil {
					return fmt.Errorf("seed: demo price history: %w", err)
				}
			}
		}
		return nil
	})
}
