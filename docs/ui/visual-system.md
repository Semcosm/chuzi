# Visual System

The values below are starting tokens. Components consume semantic tokens rather
than embedding page-specific literals. Exact values can be tuned during visual
QA, but new one-off pixel values require a documented reason.

## Semantic color tokens

Light mode:

~~~text
window-background       #F5F7FA
content-background      #FBFCFE
sidebar-background      #EEF3F9
surface                 #FFFFFF
surface-elevated        #FFFFFF
separator               #D8E0EA
text-primary            #15243A
text-secondary          #5E7089
text-tertiary           #8493A7
accent                  #2F6FDB
accent-soft             #E7F0FF
success                 #15966A
warning                 #B97918
danger                  #C84B4B
focus                   #3E83F8
~~~

Dark mode defines the same semantic roles independently; it must not be a
black/white inversion. Keep surface steps visibly distinct and separators
quiet.

## Material rules

- Window and Content backgrounds are quiet and mostly opaque.
- Sidebar is a slightly separated material, not a large floating card.
- Surface is used for list/Inspector groups that need containment.
- Elevated is reserved for menus and transient sheets.
- Glass may be used for a focused overlay or HUD only, with restrained blur.
- Borders are subtle; shadows are soft and short. Do not outline every row.

## Radius tokens

~~~text
window       18px
surface      16px
row          12px
control       9px
small         8px
pill        full
~~~

## Spacing tokens

~~~text
space-1  4px
space-2  8px
space-3 12px
space-4 16px
space-5 20px
space-6 24px
space-7 32px
~~~

Use container padding and row gaps from this scale. A different value is
allowed only when required by a native control or a measured layout boundary.

## Typography roles

~~~text
display   28-30px / semibold
title     22-24px / semibold
section   15-17px / semibold
body      13-14px / regular
secondary 12-13px / regular
caption   11-12px / regular
~~~

Typography should be calm and readable. Do not use a giant landing-page title
or bold every label.

## Control hierarchy

- One accent primary action per context.
- Secondary actions use neutral surfaces or text treatment.
- Destructive actions are muted until confirmed.
- Status uses a small dot/icon plus text; avoid oversized colored badges.
- Hit targets must remain usable on Windows and keyboard focus must be clear.

## Transitions

Use a short ease-out transition for selection, Inspector replacement, and
overlay entry. Respect reduced motion. Never animate a business state into a
different state before Core confirms the result.
