export default async function ProductPage({ params }: { params: Promise<{ id: string }> }) {
  return <div>{(await params).id}</div>;
}
