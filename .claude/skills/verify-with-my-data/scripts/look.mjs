// A quick look at one page of the running copy: each card's heading and text,
// the trade form's fields if there is one, the API calls it made, any errors,
// and a screenshot.
//
//   node look.mjs /transactions [--full]

import * as e2e from './lib.mjs'

const path = process.argv[2] || '/positions'
const full = process.argv.includes('--full')

const ctx = await e2e.open()
try {
  await e2e.go(ctx, path)
  await e2e.settle(ctx)
  for (const card of await e2e.texts(ctx, 'section.card')) {
    console.log(`— ${card.length > 400 ? card.slice(0, 400) + '…' : card}`)
  }
  if (await ctx.page.$('form .field')) console.log('\nform:', await e2e.fields(ctx))
  console.log('\napi:', ctx.net.join('\n     '))
  if (ctx.errors.length) console.log('\nerrors:', ctx.errors.join('\n        '))
  const name = path.replace(/\W+/g, '-').replace(/^-|-$/g, '') || 'root'
  console.log('\nscreenshot:', await e2e.shot(ctx, name, { fullPage: full }))
} finally {
  await e2e.close(ctx)
}
