create table rooms(
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hotel_id UUID NOT NULL,
    category_id UUID NOT NULL,
    number TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN('active', 'out_of_service')),

    CONSTRAINT unique_hotel_id_number UNIQUE (hotel_id, number),
    CONSTRAINT fk_hotels_categories FOREIGN KEY (hotel_id, category_id) REFERENCES room_categories(hotel_id, id)
);