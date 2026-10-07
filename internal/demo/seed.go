// Package demo loads a sample catalog so the storefront has something to
// show on a fresh database. It is idempotent: rows that already exist (by
// slug or SKU) are left untouched.
package demo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

type variant struct {
	sku, name  string
	attrs      map[string]string
	priceCents int64
	stock      int
}

type product struct {
	category, name, slug, brand, description string
	variants                                 []variant
}

var categories = map[string]string{
	"smartphones": "Smartphones",
	"notebooks":   "Notebooks",
	"audio":       "Áudio",
	"wearables":   "Wearables",
	"acessorios":  "Acessórios",
}

var products = []product{
	{"smartphones", "iPhone 15", "iphone-15", "Apple", "Tela Super Retina XDR de 6,1\", chip A16 Bionic e câmera principal de 48 MP.", []variant{
		{"IP15-128-PRT", "128 GB, Preto", map[string]string{"armazenamento": "128 GB", "cor": "Preto"}, 529900, 8},
		{"IP15-256-AZL", "256 GB, Azul", map[string]string{"armazenamento": "256 GB", "cor": "Azul"}, 619900, 3},
	}},
	{"smartphones", "Galaxy S24", "galaxy-s24", "Samsung", "Tela Dynamic AMOLED 2X de 6,2\", 8 GB de RAM e Galaxy AI.", []variant{
		{"GS24-256-CNZ", "256 GB, Cinza", map[string]string{"armazenamento": "256 GB", "cor": "Cinza"}, 449900, 12},
	}},
	{"smartphones", "Pixel 9", "pixel-9", "Google", "Tensor G4, sete anos de atualizações e câmera com Super Res Zoom.", []variant{
		{"PX9-128-OBS", "128 GB, Obsidian", map[string]string{"armazenamento": "128 GB", "cor": "Obsidian"}, 489900, 0},
	}},
	{"notebooks", "MacBook Air 13\" M3", "macbook-air-13-m3", "Apple", "Chip M3, 16 GB de memória unificada e até 18 horas de bateria.", []variant{
		{"MBA13-M3-16-512", "16 GB, 512 GB", map[string]string{"memoria": "16 GB", "armazenamento": "512 GB"}, 1299900, 4},
	}},
	{"notebooks", "XPS 13", "dell-xps-13", "Dell", "Intel Core Ultra 7, tela de 13,4\" e corpo em alumínio usinado.", []variant{
		{"XPS13-U7-16-1T", "16 GB, 1 TB", map[string]string{"memoria": "16 GB", "armazenamento": "1 TB"}, 1149900, 2},
	}},
	{"audio", "WH-1000XM5", "sony-wh-1000xm5", "Sony", "Fone over-ear com cancelamento de ruído e 30 horas de bateria.", []variant{
		{"WH1000XM5-PRT", "Preto", map[string]string{"cor": "Preto"}, 249900, 7},
		{"WH1000XM5-PRA", "Prata", map[string]string{"cor": "Prata"}, 249900, 1},
	}},
	{"audio", "AirPods Pro (2ª geração)", "airpods-pro-2", "Apple", "Cancelamento ativo de ruído, áudio espacial e estojo USB-C.", []variant{
		{"APP2-USBC", "Estojo USB-C", map[string]string{"conector": "USB-C"}, 199900, 15},
	}},
	{"audio", "Flip 6", "jbl-flip-6", "JBL", "Caixa de som Bluetooth à prova d'água (IP67) com 12 horas de bateria.", []variant{
		{"FLIP6-AZL", "Azul", map[string]string{"cor": "Azul"}, 69900, 20},
	}},
	{"wearables", "Apple Watch SE", "apple-watch-se", "Apple", "Detecção de queda, monitor de frequência cardíaca e GPS.", []variant{
		{"AWSE-40-MEIA", "40 mm, Meia-noite", map[string]string{"caixa": "40 mm"}, 249900, 5},
	}},
	{"acessorios", "Carregador USB-C 65 W", "anker-usb-c-65w", "Anker", "Carregador GaN compacto com duas portas USB-C e uma USB-A.", []variant{
		{"ANK-65W-GAN", "65 W", nil, 29900, 40},
	}},
	{"acessorios", "Cabo USB-C para USB-C 2 m", "baseus-cabo-usb-c-2m", "Baseus", "Cabo trançado de 100 W com transferência de dados de 480 Mb/s.", []variant{
		{"BAS-CC-2M", "2 m", nil, 5990, 60},
	}},
}

// Seed loads the sample catalog and returns how many products it created.
func Seed(ctx context.Context, db *pgxpool.Pool) (int, error) {
	created := 0
	err := postgres.WithTx(ctx, db, func(tx pgx.Tx) error {
		for slug, name := range categories {
			if _, err := tx.Exec(ctx, `INSERT INTO categories (name, slug) VALUES ($1, $2) ON CONFLICT (slug) DO NOTHING`, name, slug); err != nil {
				return fmt.Errorf("demo: category %s: %w", slug, err)
			}
		}
		for _, p := range products {
			var id string
			err := tx.QueryRow(ctx, `
				INSERT INTO products (category_id, name, slug, brand, description, status)
				SELECT id, $2, $3, $4, $5, 'active' FROM categories WHERE slug = $1
				ON CONFLICT (slug) DO NOTHING
				RETURNING id`, p.category, p.name, p.slug, p.brand, p.description).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already seeded
			}
			if err != nil {
				return fmt.Errorf("demo: product %s: %w", p.slug, err)
			}
			created++
			for _, v := range p.variants {
				attrs := v.attrs
				if attrs == nil {
					attrs = map[string]string{}
				}
				if _, err := tx.Exec(ctx, `
					WITH nv AS (
						INSERT INTO product_variants (product_id, sku, name, attributes, price_cents)
						VALUES ($1, $2, $3, $4, $5) RETURNING id
					)
					INSERT INTO inventory (variant_id, on_hand) SELECT id, $6 FROM nv`,
					id, v.sku, v.name, attrs, v.priceCents, v.stock); err != nil {
					return fmt.Errorf("demo: variant %s: %w", v.sku, err)
				}
			}
		}
		return nil
	})
	return created, err
}
