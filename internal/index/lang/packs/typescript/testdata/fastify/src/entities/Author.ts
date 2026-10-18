import { Entity, PrimaryKey, Property } from "@mikro-orm/core";

@Entity()
export class Author {
  @PrimaryKey()
  id!: number;

  @Property()
  name!: string;
}
